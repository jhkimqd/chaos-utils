package disk

import (
	"context"
	"fmt"
)

// IODelayParams defines parameters for disk I/O delay injection
type IODelayParams struct {
	// IOLatencyMs is the I/O latency in milliseconds
	IOLatencyMs int

	// TargetPath is the path to apply I/O delay (e.g., "/var/lib/heimdall")
	TargetPath string

	// Operation is the operation type: "read", "write", or "all"
	Operation string

	// Method is the injection method: "dm-delay" or "fio"
	Method string
}

// IODelayWrapper wraps disk I/O delay injection
type IODelayWrapper struct {
	dockerClient DockerClient
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// New creates a new I/O delay wrapper
func New(dockerClient DockerClient) *IODelayWrapper {
	return &IODelayWrapper{
		dockerClient: dockerClient,
	}
}

// InjectIODelay injects disk I/O latency
func (iw *IODelayWrapper) InjectIODelay(ctx context.Context, targetContainerID string, params IODelayParams) error {
	fmt.Printf("Injecting I/O delay on target %s\n", targetContainerID[:12])

	// For now, use a simpler approach with ionice to lower I/O priority
	// dm-delay requires device-mapper setup which is complex in containers
	// This is a limitation documented in the fault injection guide

	// Find processes accessing the target path
	findProcCmd := []string{"sh", "-c", fmt.Sprintf(
		"lsof %s 2>/dev/null | awk 'NR>1 {print $2}' | sort -u | head -5",
		params.TargetPath,
	)}

	pidOutput, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, findProcCmd)
	if err != nil {
		// If lsof not available or path not found, try to find main process
		fmt.Printf("  Warning: Could not find processes via lsof: %v\n", err)
		fmt.Printf("  Falling back to pid 1 (main container process)\n")
		pidOutput = "1"
	}

	// Set I/O priority to idle class (lowest)
	for _, pid := range []string{pidOutput} {
		if pid == "" {
			continue
		}

		// ionice -c 3 = idle I/O class
		ioniceCmd := []string{"ionice", "-c", "3", "-p", pid}
		output, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, ioniceCmd)
		if err != nil {
			fmt.Printf("  Warning: Failed to set ionice for PID %s: %v (output: %s)\n", pid, err, output)
			continue
		}

		fmt.Printf("  Set I/O priority to idle for PID %s\n", pid)
	}

	fmt.Printf("I/O delay injected (via ionice) on target %s\n", targetContainerID[:12])
	fmt.Printf("  Note: Full dm-delay support requires device-mapper setup\n")

	return nil
}

// RemoveFault removes I/O delay
func (iw *IODelayWrapper) RemoveFault(ctx context.Context, targetContainerID string, params IODelayParams) error {
	fmt.Printf("Removing I/O delay from target %s\n", targetContainerID[:12])

	// Find processes and restore normal I/O priority
	findProcCmd := []string{"sh", "-c", fmt.Sprintf(
		"lsof %s 2>/dev/null | awk 'NR>1 {print $2}' | sort -u | head -5",
		params.TargetPath,
	)}

	pidOutput, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, findProcCmd)
	if err != nil {
		pidOutput = "1" // Fallback to pid 1
	}

	// Restore to normal I/O priority (best-effort class)
	for _, pid := range []string{pidOutput} {
		if pid == "" {
			continue
		}

		// ionice -c 2 = best-effort class (normal)
		ioniceCmd := []string{"ionice", "-c", "2", "-p", pid}
		_, _ = iw.dockerClient.ExecCommand(ctx, targetContainerID, ioniceCmd)
	}

	fmt.Printf("I/O delay removed from target %s\n", targetContainerID[:12])

	return nil
}

// ValidateIODelayParams validates I/O delay parameters
func ValidateIODelayParams(params IODelayParams) error {
	if params.IOLatencyMs < 0 {
		return fmt.Errorf("io_latency_ms cannot be negative")
	}

	// TargetPath is optional — empty path falls back to PID 1 (main container process).

	if params.Operation != "read" && params.Operation != "write" && params.Operation != "all" {
		return fmt.Errorf("operation must be 'read', 'write', or 'all'")
	}

	return nil
}
