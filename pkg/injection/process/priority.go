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

	// Find process ID by pattern
	findPIDCmd := []string{"sh", "-c", fmt.Sprintf("pgrep -f '%s' | head -1", params.ProcessPattern)}
	pidOutput, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, findPIDCmd)
	if err != nil {
		return fmt.Errorf("failed to find process %s: %w", params.ProcessPattern, err)
	}

	pid := strings.TrimSpace(pidOutput)
	if pid == "" {
		return fmt.Errorf("no process found matching pattern: %s", params.ProcessPattern)
	}

	fmt.Printf("  Found process %s with PID %s\n", params.ProcessPattern, pid)

	// Change priority using renice
	reniceCmd := []string{"renice", "-n", fmt.Sprintf("%d", params.Priority), "-p", pid}
	output, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, reniceCmd)
	if err != nil {
		return fmt.Errorf("failed to renice process: %w (output: %s)", err, output)
	}

	fmt.Printf("Process priority changed to %d on target %s\n", params.Priority, targetContainerID[:12])

	return nil
}

// RemoveFault restores normal process priority
func (pw *PriorityWrapper) RemoveFault(ctx context.Context, targetContainerID string, params PriorityParams) error {
	fmt.Printf("Restoring normal priority on target %s\n", targetContainerID[:12])

	// Find process ID
	findPIDCmd := []string{"sh", "-c", fmt.Sprintf("pgrep -f '%s' | head -1", params.ProcessPattern)}
	pidOutput, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, findPIDCmd)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	pid := strings.TrimSpace(pidOutput)
	if pid == "" {
		// Process not found - might have been restarted, that's fine
		fmt.Printf("  Process not found (may have been restarted)\n")
		return nil
	}

	// Restore to normal priority (0)
	reniceCmd := []string{"renice", "-n", "0", "-p", pid}
	output, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, reniceCmd)
	if err != nil {
		return fmt.Errorf("failed to restore priority: %w (output: %s)", err, output)
	}

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
