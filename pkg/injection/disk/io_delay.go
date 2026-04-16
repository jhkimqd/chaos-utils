package disk

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// IODelayParams defines parameters for disk I/O delay injection.
//
// The Method field selects between two fundamentally different approaches:
//   - "dd" (default): Spawns background dd processes to create I/O contention.
//     IOLatencyMs controls worker count (not actual latency): <100ms=1, 100-199=2, 200+=3.
//   - "dm-delay": Uses Linux device-mapper to add deterministic kernel-level latency.
//     IOLatencyMs is the exact delay in milliseconds applied to every I/O operation.
//     Requires privileged container with dmsetup installed.
type IODelayParams struct {
	// IOLatencyMs has different semantics per method:
	//   - "dd": Controls contention intensity (worker count scaling, not precise latency)
	//   - "dm-delay": Exact I/O delay in milliseconds applied at the block device level
	IOLatencyMs int

	// TargetPath is the filesystem path to target (e.g., "/var/lib/bor/bor/chaindata").
	// For "dd": directory where contention files are written.
	// For "dm-delay": used to discover the backing block device.
	TargetPath string

	// Operation is the operation type: "read", "write", or "all" (dd method only;
	// dm-delay always delays both reads and writes)
	Operation string

	// Method selects the injection approach: "dd" (default), "dm-delay", or "" (uses dd).
	Method string
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// IODelayWrapper wraps disk I/O delay injection
type IODelayWrapper struct {
	dockerClient DockerClient
	dmDelay      *DmDelayWrapper
}

// New creates a new I/O delay wrapper
func New(dockerClient DockerClient) *IODelayWrapper {
	return &IODelayWrapper{
		dockerClient: dockerClient,
		dmDelay:      NewDmDelayWrapper(dockerClient),
	}
}

// InjectIODelay injects I/O delay using the configured method.
// Method "dm-delay" uses kernel-level device-mapper delay for deterministic latency.
// Method "dd" (default) creates I/O contention by running background dd processes.
func (iw *IODelayWrapper) InjectIODelay(ctx context.Context, targetContainerID string, params IODelayParams) error {
	if params.Method == "dm-delay" {
		return iw.dmDelay.InjectDmDelay(ctx, targetContainerID, params)
	}

	fmt.Printf("Injecting I/O contention on target %s\n", targetContainerID[:12])

	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}

	// dd method: scale worker count based on IOLatencyMs intensity.
	// <100 = 1 worker, 100-199 = 2 workers, 200+ = 3 workers
	workers := 1
	if params.IOLatencyMs >= 200 {
		workers = 3
	} else if params.IOLatencyMs >= 100 {
		workers = 2
	}

	// Build the I/O stress command based on operation type
	var stressCmd string
	chaosFile := fmt.Sprintf("%s/.chaos_io_stress", strings.TrimRight(targetPath, "/"))

	switch params.Operation {
	case "write":
		// Continuous write loop — fills I/O write queue
		stressCmd = fmt.Sprintf(
			"for i in $(seq 1 %d); do while true; do dd if=/dev/zero of=\"%s_$i\" bs=64K count=256 conv=fdatasync 2>/dev/null; done & done",
			workers, chaosFile,
		)
	case "read":
		// Create a file then continuously read it — fills I/O read queue
		stressCmd = fmt.Sprintf(
			"dd if=/dev/zero of=\"%s_src\" bs=1M count=16 2>/dev/null; for i in $(seq 1 %d); do while true; do dd if=\"%s_src\" of=/dev/null bs=64K 2>/dev/null; done & done",
			chaosFile, workers, chaosFile,
		)
	default: // "all"
		// Both read and write contention
		stressCmd = fmt.Sprintf(
			"dd if=/dev/zero of=\"%s_src\" bs=1M count=16 2>/dev/null; "+
				"for i in $(seq 1 %d); do while true; do dd if=/dev/zero of=\"%s_w$i\" bs=64K count=256 conv=fdatasync 2>/dev/null; done & done; "+
				"for i in $(seq 1 %d); do while true; do dd if=\"%s_src\" of=/dev/null bs=64K 2>/dev/null; done & done",
			chaosFile, workers, chaosFile, workers, chaosFile,
		)
	}

	cmd := []string{"sh", "-c", stressCmd}
	_, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to start I/O contention: %w", err)
	}

	// Verify dd processes are running
	verifyCmd := []string{"sh", "-c",
		"COUNT=0; for p in /proc/[0-9]*/cmdline; do if tr '\\0' ' ' < $p 2>/dev/null | grep -q '^dd '; then COUNT=$((COUNT+1)); fi; done; echo $COUNT",
	}
	countOutput, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
	if err != nil {
		return fmt.Errorf("failed to verify I/O contention: %w", err)
	}

	count := strings.TrimSpace(countOutput)
	if count == "" || count == "0" {
		return fmt.Errorf("I/O contention failed: no dd processes running")
	}

	fmt.Printf("  I/O contention active: %s dd workers on %s (%s)\n", count, targetPath, params.Operation)
	return nil
}

// RemoveFault removes I/O delay faults. If a dm-delay mapping is active for the
// container, it is removed and the function returns. Otherwise, dd stress
// processes and temp files are cleaned up.
func (iw *IODelayWrapper) RemoveFault(ctx context.Context, targetContainerID string, params IODelayParams) error {
	// Remove dm-delay mapping if one exists for this container
	if iw.dmDelay != nil && iw.dmDelay.HasActiveMapping(targetContainerID) {
		if err := iw.dmDelay.RemoveDmDelay(ctx, targetContainerID); err != nil {
			return err
		}
		// dm-delay was the active method — no dd cleanup needed
		return nil
	}

	fmt.Printf("Removing I/O contention from target %s\n", targetContainerID[:12])

	// Kill chaos dd processes AND their parent shell loops.
	// Only match processes with "chaos_io_stress" in their args to avoid killing
	// unrelated dd processes (e.g., backup scripts).
	killCmd := []string{"sh", "-c",
		"for p in /proc/[0-9]*/cmdline; do " +
			"PID=$(echo $p | cut -d/ -f3); " +
			"CMD=$(tr '\\0' ' ' < $p 2>/dev/null); " +
			"case \"$CMD\" in " +
			"*chaos_io_stress*) kill -9 $PID 2>/dev/null ;; " +
			"esac; " +
			"done; echo done",
	}
	_, killErr := iw.dockerClient.ExecCommand(ctx, targetContainerID, killCmd)
	if killErr != nil {
		log.Warn().Err(killErr).Str("container", targetContainerID[:12]).Msg("failed to kill dd processes during I/O contention removal")
	}

	// Always verify processes are actually gone — the kill script swallows individual
	// kill errors via 2>/dev/null, so killErr being nil doesn't guarantee success.
	verifyCmd := []string{"sh", "-c",
		"COUNT=0; for p in /proc/[0-9]*/cmdline; do if tr '\\0' ' ' < $p 2>/dev/null | grep -qE 'chaos_io_stress|^dd .*chaos_io_stress'; then COUNT=$((COUNT+1)); fi; done; echo $COUNT",
	}
	countOutput, verifyErr := iw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
	if verifyErr == nil {
		count := strings.TrimSpace(countOutput)
		if count != "" && count != "0" {
			return fmt.Errorf("failed to remove I/O contention: %s processes still running after kill", count)
		}
	} else {
		// Verify command itself failed — can't confirm processes are gone
		log.Warn().Err(verifyErr).Str("container", targetContainerID[:12]).Msg("failed to verify I/O contention removal")
	}

	// Clean up stress files
	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}
	chaosFile := fmt.Sprintf("%s/.chaos_io_stress", strings.TrimRight(targetPath, "/"))
	cleanCmd := []string{"sh", "-c", fmt.Sprintf("rm -f \"%s\"_* 2>/dev/null; echo done", chaosFile)}
	_, cleanErr := iw.dockerClient.ExecCommand(ctx, targetContainerID, cleanCmd)
	if cleanErr != nil {
		log.Warn().Err(cleanErr).Str("container", targetContainerID[:12]).Msg("failed to clean up I/O stress files")
	}

	fmt.Printf("  I/O contention removed from target %s\n", targetContainerID[:12])
	return nil
}

// ValidateIODelayParams validates I/O delay parameters
func ValidateIODelayParams(params IODelayParams) error {
	if params.IOLatencyMs < 0 {
		return fmt.Errorf("io_latency_ms cannot be negative")
	}

	if params.Operation != "read" && params.Operation != "write" && params.Operation != "all" {
		return fmt.Errorf("operation must be 'read', 'write', or 'all'")
	}

	if params.Method != "" && params.Method != "dd" && params.Method != "dm-delay" {
		return fmt.Errorf("unsupported method '%s'; valid values: 'dd', 'dm-delay', or '' (empty)", params.Method)
	}

	return nil
}
