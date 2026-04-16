package disk

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// DmDelayWrapper manages dm-delay device-mapper mappings for I/O delay injection.
// Unlike the dd-based approach (which saturates the I/O queue with contention),
// dm-delay inserts deterministic, kernel-level latency on every I/O operation
// to the backing block device.
type DmDelayWrapper struct {
	dockerClient DockerClient
	mu           sync.Mutex
	// activeMappings tracks the dm-delay mapping name per container for cleanup.
	// Key: containerID, Value: dmsetup mapping name (e.g., "chaos-delay-abcdef12").
	activeMappings map[string]string
}

// NewDmDelayWrapper creates a new dm-delay wrapper.
func NewDmDelayWrapper(dockerClient DockerClient) *DmDelayWrapper {
	return &DmDelayWrapper{
		dockerClient:   dockerClient,
		activeMappings: make(map[string]string),
	}
}

// mappingName returns a unique dm-delay mapping name for a container.
func mappingName(containerID string) string {
	short := containerID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("chaos-delay-%s", short)
}

// preflightCheck verifies the container has dmsetup available and sufficient privileges.
func (dw *DmDelayWrapper) preflightCheck(ctx context.Context, containerID string) error {
	// Check dmsetup is available
	out, err := dw.dockerClient.ExecCommand(ctx, containerID, []string{"sh", "-c", "dmsetup version 2>&1"})
	if err != nil {
		return fmt.Errorf("dmsetup not available in container %s (is device-mapper installed?): %w", containerID[:12], err)
	}
	if strings.Contains(out, "not found") || strings.Contains(out, "No such file") {
		return fmt.Errorf("dmsetup not available in container %s: %s", containerID[:12], strings.TrimSpace(out))
	}

	// Check /dev/mapper exists (indicates device-mapper kernel support + privileges)
	_, err = dw.dockerClient.ExecCommand(ctx, containerID, []string{"sh", "-c", "test -d /dev/mapper"})
	if err != nil {
		return fmt.Errorf("container %s lacks device-mapper support or is not privileged: %w", containerID[:12], err)
	}

	return nil
}

// discoverDevice finds the backing block device for a given path inside the container.
func (dw *DmDelayWrapper) discoverDevice(ctx context.Context, containerID string, targetPath string) (string, error) {
	// Try findmnt first (more reliable, gives the source device directly)
	cmd := []string{"sh", "-c", fmt.Sprintf("findmnt -no SOURCE %q 2>/dev/null || df %q 2>/dev/null | tail -1 | awk '{print $1}'", targetPath, targetPath)}
	out, err := dw.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to discover block device for %s: %w", targetPath, err)
	}

	device := strings.TrimSpace(out)
	if device == "" {
		return "", fmt.Errorf("could not determine block device for path %s", targetPath)
	}

	// Validate it looks like a device path
	if !strings.HasPrefix(device, "/dev/") {
		return "", fmt.Errorf("discovered device %q for path %s does not look like a block device", device, targetPath)
	}

	return device, nil
}

// getDeviceSectors returns the size of the block device in 512-byte sectors.
func (dw *DmDelayWrapper) getDeviceSectors(ctx context.Context, containerID string, device string) (string, error) {
	cmd := []string{"sh", "-c", fmt.Sprintf("blockdev --getsz %s", device)}
	out, err := dw.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get device size for %s: %w", device, err)
	}

	sectors := strings.TrimSpace(out)
	if sectors == "" || sectors == "0" {
		return "", fmt.Errorf("device %s reports 0 sectors", device)
	}

	return sectors, nil
}

// InjectDmDelay creates a dm-delay device-mapper mapping that adds deterministic
// I/O latency to all operations on the block device backing params.TargetPath.
func (dw *DmDelayWrapper) InjectDmDelay(ctx context.Context, containerID string, params IODelayParams) error {
	fmt.Printf("Injecting dm-delay on target %s\n", containerID[:12])

	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}

	// Check if there's already an active mapping for this container
	dw.mu.Lock()
	if existing, ok := dw.activeMappings[containerID]; ok {
		dw.mu.Unlock()
		return fmt.Errorf("dm-delay mapping %q already active for container %s; remove it first", existing, containerID[:12])
	}
	dw.mu.Unlock()

	// Step 1: Preflight — verify dmsetup is available and container is privileged
	if err := dw.preflightCheck(ctx, containerID); err != nil {
		return err
	}

	// Step 2: Discover the backing block device
	device, err := dw.discoverDevice(ctx, containerID, targetPath)
	if err != nil {
		return err
	}
	fmt.Printf("  Backing device for %s: %s\n", targetPath, device)

	// Step 3: Get device size in sectors
	sectors, err := dw.getDeviceSectors(ctx, containerID, device)
	if err != nil {
		return err
	}

	// Step 4: Create dm-delay mapping
	name := mappingName(containerID)
	table := fmt.Sprintf("0 %s delay %s 0 %d", sectors, device, params.IOLatencyMs)
	createCmd := []string{"sh", "-c", fmt.Sprintf("dmsetup create %s --table %q", name, table)}
	_, err = dw.dockerClient.ExecCommand(ctx, containerID, createCmd)
	if err != nil {
		return fmt.Errorf("failed to create dm-delay mapping %s: %w", name, err)
	}

	// Step 5: Verify the mapping exists
	verifyCmd := []string{"sh", "-c", fmt.Sprintf("dmsetup status %s", name)}
	statusOut, err := dw.dockerClient.ExecCommand(ctx, containerID, verifyCmd)
	if err != nil {
		// Mapping creation succeeded but verification failed — try to clean up
		log.Warn().Err(err).Str("mapping", name).Msg("dm-delay verify failed after create; attempting cleanup")
		cleanupCmd := []string{"sh", "-c", fmt.Sprintf("dmsetup remove %s 2>/dev/null", name)}
		_, cleanupErr := dw.dockerClient.ExecCommand(ctx, containerID, cleanupCmd)
		if cleanupErr != nil {
			log.Warn().Err(cleanupErr).Str("container", containerID[:12]).Str("mapping", name).Msg("failed to clean up dm-delay mapping after verify failure")
		}
		return fmt.Errorf("dm-delay mapping %s created but verification failed: %w", name, err)
	}

	// Track the active mapping
	dw.mu.Lock()
	dw.activeMappings[containerID] = name
	dw.mu.Unlock()

	fmt.Printf("  dm-delay active: %s (%s, %dms delay)\n", name, strings.TrimSpace(statusOut), params.IOLatencyMs)
	return nil
}

// RemoveDmDelay removes the dm-delay device-mapper mapping for a container.
func (dw *DmDelayWrapper) RemoveDmDelay(ctx context.Context, containerID string) error {
	dw.mu.Lock()
	name, ok := dw.activeMappings[containerID]
	if !ok {
		dw.mu.Unlock()
		// No active mapping — nothing to do
		return nil
	}
	dw.mu.Unlock()

	fmt.Printf("Removing dm-delay mapping %s from target %s\n", name, containerID[:12])

	// Remove the mapping
	removeCmd := []string{"sh", "-c", fmt.Sprintf("dmsetup remove %s", name)}
	_, err := dw.dockerClient.ExecCommand(ctx, containerID, removeCmd)
	if err != nil {
		log.Warn().Err(err).Str("mapping", name).Str("container", containerID[:12]).Msg("failed to remove dm-delay mapping")
		return fmt.Errorf("failed to remove dm-delay mapping %s: %w", name, err)
	}

	// Verify removal
	verifyCmd := []string{"sh", "-c", fmt.Sprintf("dmsetup status %s 2>&1", name)}
	out, verifyErr := dw.dockerClient.ExecCommand(ctx, containerID, verifyCmd)
	if verifyErr == nil && !strings.Contains(out, "No such device") && !strings.Contains(out, "not found") && out != "" {
		log.Warn().Str("mapping", name).Str("status", strings.TrimSpace(out)).Msg("dm-delay mapping still exists after remove")
		return fmt.Errorf("dm-delay mapping %s still exists after removal", name)
	}

	// Clear from tracking
	dw.mu.Lock()
	delete(dw.activeMappings, containerID)
	dw.mu.Unlock()

	fmt.Printf("  dm-delay mapping %s removed from target %s\n", name, containerID[:12])
	return nil
}

// HasActiveMapping returns true if the container has an active dm-delay mapping.
func (dw *DmDelayWrapper) HasActiveMapping(containerID string) bool {
	dw.mu.Lock()
	defer dw.mu.Unlock()
	_, ok := dw.activeMappings[containerID]
	return ok
}
