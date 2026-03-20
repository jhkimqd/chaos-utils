package sidecar

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// Manager manages chaos-utils sidecar containers
type Manager struct {
	dockerClient  *docker.Client
	sidecarImage  string
	createdSidecars map[string]string // target container ID -> sidecar container ID
}

// New creates a new sidecar manager
func New(dockerClient *docker.Client, sidecarImage string) *Manager {
	return &Manager{
		dockerClient:    dockerClient,
		sidecarImage:    sidecarImage,
		createdSidecars: make(map[string]string),
	}
}

// CreateSidecar creates and attaches a sidecar to a target container's network namespace
func (m *Manager) CreateSidecar(ctx context.Context, targetContainerID string) (string, error) {
	// Reuse existing sidecar if one is already running for this target
	if sidecarID, exists := m.createdSidecars[targetContainerID]; exists {
		fmt.Printf("Reusing existing sidecar %s for target %s\n", sidecarID[:12], targetContainerID[:12])
		return sidecarID, nil
	}

	// Pull sidecar image if not available locally
	if err := m.dockerClient.EnsureImage(ctx, m.sidecarImage); err != nil {
		return "", fmt.Errorf("sidecar image unavailable: %w", err)
	}

	// Create sidecar container with network namespace sharing
	sidecarName := fmt.Sprintf("chaos-sidecar-%s", targetContainerID[:12])

	config := &container.Config{
		Image: m.sidecarImage,
		// Keep container running
		Cmd: []string{"sleep", "infinity"},
		Tty: true,
	}

	hostConfig := &container.HostConfig{
		// Share network namespace with target
		NetworkMode: container.NetworkMode(fmt.Sprintf("container:%s", targetContainerID)),
		// Grant network admin capabilities
		CapAdd: []string{"NET_ADMIN", "NET_RAW"},
		// Auto-remove when stopped
		AutoRemove: true,
	}

	networkingConfig := &network.NetworkingConfig{}

	resp, err := m.dockerClient.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, sidecarName)
	if err != nil {
		return "", fmt.Errorf("failed to create sidecar container: %w", err)
	}

	sidecarID := resp.ID

	// Start the sidecar
	if err := m.dockerClient.ContainerStart(ctx, sidecarID, types.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start sidecar container: %w", err)
	}

	// Track the sidecar
	m.createdSidecars[targetContainerID] = sidecarID

	fmt.Printf("Created sidecar %s for target %s\n", sidecarID[:12], targetContainerID[:12])

	return sidecarID, nil
}

// DestroySidecar removes a sidecar container
func (m *Manager) DestroySidecar(ctx context.Context, targetContainerID string) error {
	sidecarID, exists := m.createdSidecars[targetContainerID]
	if !exists {
		// Sidecar already cleaned up or never existed - this is not an error
		// This makes cleanup idempotent and prevents duplicate cleanup errors
		return nil
	}

	fmt.Printf("Destroying sidecar %s for target %s\n", sidecarID[:12], targetContainerID[:12])

	// Stop the container (will auto-remove due to AutoRemove flag)
	timeout := 10
	if err := m.dockerClient.ContainerStop(ctx, sidecarID, &timeout); err != nil {
		// NotFound or NotModified (already stopped) are expected during cleanup
		if !errdefs.IsNotFound(err) && !errdefs.IsNotModified(err) {
			return fmt.Errorf("failed to stop sidecar: %w", err)
		}
	}

	// Force remove just in case
	if err := m.dockerClient.ContainerRemove(ctx, sidecarID, types.ContainerRemoveOptions{
		Force: true,
	}); err != nil {
		// NotFound (already gone) or Conflict (removal in progress) are expected
		if !errdefs.IsNotFound(err) && !errdefs.IsConflict(err) {
			return fmt.Errorf("failed to remove sidecar: %w", err)
		}
	}

	// Remove from tracking
	delete(m.createdSidecars, targetContainerID)

	fmt.Printf("Destroyed sidecar for target %s\n", targetContainerID[:12])

	return nil
}

// ExecInSidecar executes a command in a sidecar container
func (m *Manager) ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error) {
	sidecarID, exists := m.createdSidecars[targetContainerID]
	if !exists {
		return "", fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Executing in sidecar %s: %s\n", sidecarID[:12], strings.Join(cmd, " "))

	output, err := m.dockerClient.ExecCommand(ctx, sidecarID, cmd)
	if err != nil {
		return output, fmt.Errorf("failed to execute command in sidecar: %w", err)
	}

	return output, nil
}

// GetSidecarID returns the sidecar ID for a target container
func (m *Manager) GetSidecarID(targetContainerID string) (string, bool) {
	sidecarID, exists := m.createdSidecars[targetContainerID]
	return sidecarID, exists
}

// ListSidecars returns all created sidecars
func (m *Manager) ListSidecars() map[string]string {
	result := make(map[string]string)
	for target, sidecar := range m.createdSidecars {
		result[target] = sidecar
	}
	return result
}

