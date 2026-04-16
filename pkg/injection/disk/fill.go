package disk

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// FillParams defines parameters for disk space fill injection
type FillParams struct {
	// TargetPath is the directory to fill (e.g., "/root/.bor/data")
	TargetPath string

	// SizeMB is the amount of data to write in MB (0 = fill to percentage)
	SizeMB int

	// FillPercent is the target disk usage percentage (0-100, used when SizeMB is 0)
	FillPercent int

	// FileName is the name of the fill file (default: "chaos_fill_data")
	FileName string
}

// FillWrapper wraps disk fill injection
type FillWrapper struct {
	dockerClient  DockerClient
	mu            sync.Mutex
	injectedFills map[string][]string // tracks fill file paths per container for cleanup
}

// NewFillWrapper creates a new disk fill wrapper
func NewFillWrapper(dockerClient DockerClient) *FillWrapper {
	return &FillWrapper{
		dockerClient:  dockerClient,
		injectedFills: make(map[string][]string),
	}
}

// InjectDiskFill fills disk space on a target container
func (fw *FillWrapper) InjectDiskFill(ctx context.Context, targetContainerID string, params FillParams) error {
	fmt.Printf("Injecting disk fill on target %s\n", targetContainerID[:12])

	fileName := params.FileName
	if fileName == "" {
		fileName = "chaos_fill_data"
	}

	fillPath := fmt.Sprintf("%s/%s", strings.TrimRight(params.TargetPath, "/"), fileName)

	if params.SizeMB > 0 {
		// Write a specific amount of data
		cmd := []string{"sh", "-c", fmt.Sprintf(
			"fallocate -l %dM \"%s\" 2>/dev/null || dd if=/dev/zero of=\"%s\" bs=1M count=%d 2>/dev/null",
			params.SizeMB, fillPath, fillPath, params.SizeMB,
		)}

		output, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
		if err != nil {
			return fmt.Errorf("failed to fill disk: %w (output: %s)", err, output)
		}

		fmt.Printf("  Wrote %dMB to %s\n", params.SizeMB, fillPath)
	} else if params.FillPercent > 0 {
		// Fill to a percentage of available space
		// Use simple shell arithmetic (no awk) for Alpine/BusyBox compatibility
		getSpaceCmd := []string{"sh", "-c", fmt.Sprintf(
			"set -- $(df -m \"%s\" | tail -1); total=$2; used=$3; target=$(( total * %d / 100 )); to_write=$(( target - used )); if [ $to_write -gt 0 ]; then echo $to_write; else echo 0; fi",
			params.TargetPath, params.FillPercent,
		)}

		sizeOutput, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, getSpaceCmd)
		if err != nil {
			return fmt.Errorf("failed to calculate fill size: %w", err)
		}

		sizeMB := strings.TrimSpace(sizeOutput)
		if sizeMB == "" || sizeMB == "0" {
			fmt.Printf("  Disk already at or above %d%% usage, skipping\n", params.FillPercent)
			return nil
		}

		cmd := []string{"sh", "-c", fmt.Sprintf(
			"fallocate -l %sM \"%s\" 2>/dev/null || dd if=/dev/zero of=\"%s\" bs=1M count=%s 2>/dev/null",
			sizeMB, fillPath, fillPath, sizeMB,
		)}

		output, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
		if err != nil {
			return fmt.Errorf("failed to fill disk to %d%%: %w (output: %s)", params.FillPercent, err, output)
		}

		fmt.Printf("  Wrote %sMB to %s (target: %d%% usage)\n", sizeMB, fillPath, params.FillPercent)
	} else {
		return fmt.Errorf("either size_mb or fill_percent must be specified")
	}

	// Track for cleanup
	fw.mu.Lock()
	fw.injectedFills[targetContainerID] = append(fw.injectedFills[targetContainerID], fillPath)
	fw.mu.Unlock()

	fmt.Printf("Disk fill injected on target %s\n", targetContainerID[:12])
	return nil
}

// RemoveFault removes all fill files for a container (used by injector cleanup)
func (fw *FillWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	fw.mu.Lock()
	paths, exists := fw.injectedFills[targetContainerID]
	fw.mu.Unlock()
	if !exists || len(paths) == 0 {
		// Fallback: try to remove fill files from common locations only
		// (avoid dangerous system-wide 'find / -delete')
		cmd := []string{"sh", "-c", "rm -f /tmp/chaos_fill_data /var/lib/*/chaos_fill_data /root/chaos_fill_data 2>/dev/null; echo done"}
		_, cleanupErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
		if cleanupErr != nil {
			log.Warn().Err(cleanupErr).Str("container", targetContainerID[:12]).Msg("failed to remove fallback fill files during cleanup")
		}
		return nil
	}

	for _, path := range paths {
		cmd := []string{"rm", "-f", path}
		_, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
		if err != nil {
			fmt.Printf("  Warning: Failed to remove fill file %s: %v\n", path, err)
		} else {
			fmt.Printf("  Removed fill file %s\n", path)
		}
	}

	fw.mu.Lock()
	delete(fw.injectedFills, targetContainerID)
	fw.mu.Unlock()
	return nil
}

// ValidateFillParams validates disk fill parameters
func ValidateFillParams(params FillParams) error {
	if params.TargetPath == "" {
		return fmt.Errorf("target_path must be specified")
	}

	if params.SizeMB < 0 {
		return fmt.Errorf("size_mb cannot be negative")
	}

	if params.FillPercent < 0 || params.FillPercent > 100 {
		return fmt.Errorf("fill_percent must be between 0 and 100")
	}

	if params.SizeMB == 0 && params.FillPercent == 0 {
		return fmt.Errorf("either size_mb or fill_percent must be specified")
	}

	return nil
}
