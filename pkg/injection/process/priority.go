package process

import (
	"context"
	"fmt"
	"strings"
)

// PriorityParams defines parameters for process priority manipulation
type PriorityParams struct {
	// Priority is the nice value (-20 to 19, where 19 is lowest priority)
	Priority int

	// ProcessPattern is the pattern to match process names (e.g., "heimdalld", "bor")
	ProcessPattern string
}

// PriorityWrapper wraps process priority manipulation
type PriorityWrapper struct {
	dockerClient DockerClient
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// New creates a new priority wrapper
func New(dockerClient DockerClient) *PriorityWrapper {
	return &PriorityWrapper{
		dockerClient: dockerClient,
	}
}

// InjectPriorityChange changes process priority (renice)
func (pw *PriorityWrapper) InjectPriorityChange(ctx context.Context, targetContainerID string, params PriorityParams) error {
	fmt.Printf("Changing process priority on target %s\n", targetContainerID[:12])

	// Find process ID by pattern using /proc scanning (BusyBox-compatible, no pgrep needed)
	findPIDCmd := []string{"sh", "-c", fmt.Sprintf(
		"for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); if tr '\\0' ' ' < $p 2>/dev/null | grep -q '%s'; then echo $PID; exit 0; fi; done; echo ''",
		params.ProcessPattern,
	)}
	pidOutput, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, findPIDCmd)
	if err != nil {
		return fmt.Errorf("failed to find process %s: %w", params.ProcessPattern, err)
	}

	pid := strings.TrimSpace(pidOutput)
	if pid == "" {
		return fmt.Errorf("no process found matching pattern: %s", params.ProcessPattern)
	}

	fmt.Printf("  Found process %s with PID %s\n", params.ProcessPattern, pid)

	// Change priority using renice. BusyBox renice uses: renice PRIORITY PID
	// GNU renice uses: renice -n PRIORITY -p PID
	// Try BusyBox syntax first, fall back to GNU
	reniceCmd := []string{"sh", "-c", fmt.Sprintf(
		"renice %d %s 2>/dev/null || renice -n %d -p %s 2>/dev/null || echo FAIL",
		params.Priority, pid, params.Priority, pid,
	)}
	output, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, reniceCmd)
	if err != nil || strings.Contains(output, "FAIL") {
		return fmt.Errorf("failed to renice process %s: %w (output: %s)", pid, err, output)
	}

	fmt.Printf("Process priority changed to %d on target %s\n", params.Priority, targetContainerID[:12])

	return nil
}

// RemoveFault restores normal process priority
func (pw *PriorityWrapper) RemoveFault(ctx context.Context, targetContainerID string, params PriorityParams) error {
	fmt.Printf("Restoring normal priority on target %s\n", targetContainerID[:12])

	// Find process ID using /proc scanning (BusyBox-compatible)
	findPIDCmd := []string{"sh", "-c", fmt.Sprintf(
		"for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); if tr '\\0' ' ' < $p 2>/dev/null | grep -q '%s'; then echo $PID; exit 0; fi; done; echo ''",
		params.ProcessPattern,
	)}
	pidOutput, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, findPIDCmd)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	pid := strings.TrimSpace(pidOutput)
	if pid == "" {
		fmt.Printf("  Process not found (may have been restarted)\n")
		return nil
	}

	// Restore to normal priority (0) — try BusyBox then GNU syntax
	reniceCmd := []string{"sh", "-c", fmt.Sprintf(
		"renice 0 %s 2>/dev/null || renice -n 0 -p %s 2>/dev/null || true", pid, pid,
	)}
	_, _ = pw.dockerClient.ExecCommand(ctx, targetContainerID, reniceCmd)

	fmt.Printf("Normal priority restored on target %s\n", targetContainerID[:12])
	return nil
}

// ValidatePriorityParams validates priority parameters
func ValidatePriorityParams(params PriorityParams) error {
	if params.Priority < -20 || params.Priority > 19 {
		return fmt.Errorf("priority must be between -20 and 19")
	}

	if params.ProcessPattern == "" {
		return fmt.Errorf("process_pattern must be specified")
	}

	return nil
}
