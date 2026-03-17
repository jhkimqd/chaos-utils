package disk

import (
	"context"
	"fmt"
	"strings"
)

// IODelayParams defines parameters for disk I/O delay injection
type IODelayParams struct {
	// IOLatencyMs controls the intensity of I/O contention. Higher values spawn
	// more aggressive background I/O workers. Not a precise latency value.
	IOLatencyMs int

	// TargetPath is the directory where I/O contention is generated (e.g., "/var/lib/bor/bor/chaindata")
	TargetPath string

	// Operation is the operation type: "read", "write", or "all"
	Operation string

	// Method is the injection method (ignored — always uses background dd contention)
	Method string
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// IODelayWrapper wraps disk I/O delay injection
type IODelayWrapper struct {
	dockerClient DockerClient
}

// New creates a new I/O delay wrapper
func New(dockerClient DockerClient) *IODelayWrapper {
	return &IODelayWrapper{
		dockerClient: dockerClient,
	}
}

// InjectIODelay creates real I/O contention by running background dd processes
// that continuously read/write to the target path. This saturates the I/O queue
// and forces the target application to compete for disk bandwidth.
func (iw *IODelayWrapper) InjectIODelay(ctx context.Context, targetContainerID string, params IODelayParams) error {
	fmt.Printf("Injecting I/O contention on target %s\n", targetContainerID[:12])

	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}

	// Scale number of workers based on IOLatencyMs:
	// 50-100ms = 1 worker, 100-200ms = 2 workers, 200+ = 3 workers
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

// RemoveFault kills dd stress processes and cleans up temp files
func (iw *IODelayWrapper) RemoveFault(ctx context.Context, targetContainerID string, params IODelayParams) error {
	fmt.Printf("Removing I/O contention from target %s\n", targetContainerID[:12])

	// Kill dd processes AND their parent shell loops (while true; do dd ...; done).
	// Match both "dd " and "while" shells that contain "chaos_io_stress" in their args.
	killCmd := []string{"sh", "-c",
		"for p in /proc/[0-9]*/cmdline; do " +
			"PID=$(echo $p | cut -d/ -f3); " +
			"CMD=$(tr '\\0' ' ' < $p 2>/dev/null); " +
			"case \"$CMD\" in " +
			"*chaos_io_stress*|\"dd \"*) kill -9 $PID 2>/dev/null ;; " +
			"esac; " +
			"done; echo done",
	}
	_, _ = iw.dockerClient.ExecCommand(ctx, targetContainerID, killCmd)

	// Clean up stress files
	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}
	chaosFile := fmt.Sprintf("%s/.chaos_io_stress", strings.TrimRight(targetPath, "/"))
	cleanCmd := []string{"sh", "-c", fmt.Sprintf("rm -f \"%s\"_* 2>/dev/null; echo done", chaosFile)}
	_, _ = iw.dockerClient.ExecCommand(ctx, targetContainerID, cleanCmd)

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

	return nil
}
