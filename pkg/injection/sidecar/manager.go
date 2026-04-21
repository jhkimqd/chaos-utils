package sidecar

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// Manager manages chaos-utils sidecar containers.
//
// Concurrency (F-14): createdSidecars is read and mutated from multiple
// goroutines — the main orchestrator Execute path AND the emergency-stop
// signal watcher goroutine both reach CleanupAll → DestroySidecar under
// fault conditions, which previously raced on `delete(m.createdSidecars, …)`
// and tripped Go's "concurrent map writes" fatal. All map accesses are now
// guarded by mu. We hold mu only around map ops — never across docker API
// I/O — to keep parallel sidecar create/destroy on distinct targets fast
// and to avoid deadlock against docker's own locking. Reads outnumber
// writes (GetSidecarID / ExecInSidecar lookups fire on every fault op)
// so an RWMutex is a good fit.
type Manager struct {
	dockerClient  *docker.Client
	sidecarImage  string
	mu              sync.RWMutex
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
	// Reuse existing sidecar if one is already running for this target.
	// RLock for the cheap lookup; we re-check under Lock later to catch the
	// race where two goroutines both see "not exists" and both proceed to
	// create.
	m.mu.RLock()
	if sidecarID, exists := m.createdSidecars[targetContainerID]; exists {
		m.mu.RUnlock()
		fmt.Printf("Reusing existing sidecar %s for target %s\n", sidecarID[:12], targetContainerID[:12])
		return sidecarID, nil
	}
	m.mu.RUnlock()

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

	// Track the sidecar. Re-check under Lock — another goroutine may have
	// raced past the RLock miss above and registered its own sidecar for
	// this target. If so, the first writer wins; our container becomes a
	// duplicate and we tear it down out-of-band so we don't leak. Rare in
	// practice (createSidecar per target is serialised by TCWrapper's
	// ensureSidecar in the current call graph), but cheap to handle.
	m.mu.Lock()
	if existing, raced := m.createdSidecars[targetContainerID]; raced {
		m.mu.Unlock()
		fmt.Printf("Lost create race for target %s, destroying duplicate sidecar %s\n",
			targetContainerID[:12], sidecarID[:12])
		go m.destroyOrphanSidecar(sidecarID)
		return existing, nil
	}
	m.createdSidecars[targetContainerID] = sidecarID
	m.mu.Unlock()

	fmt.Printf("Created sidecar %s for target %s\n", sidecarID[:12], targetContainerID[:12])

	return sidecarID, nil
}

// DestroySidecar removes a sidecar container
func (m *Manager) DestroySidecar(ctx context.Context, targetContainerID string) error {
	m.mu.RLock()
	sidecarID, exists := m.createdSidecars[targetContainerID]
	m.mu.RUnlock()
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

	// Remove from tracking. delete() on an absent key is a no-op, so a
	// second concurrent DestroySidecar that races us to this point is safe
	// as long as both callers serialise through mu.
	m.mu.Lock()
	delete(m.createdSidecars, targetContainerID)
	m.mu.Unlock()

	fmt.Printf("Destroyed sidecar for target %s\n", targetContainerID[:12])

	return nil
}

// ExecInSidecar executes a command in a sidecar container
func (m *Manager) ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error) {
	m.mu.RLock()
	sidecarID, exists := m.createdSidecars[targetContainerID]
	m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	sidecarID, exists := m.createdSidecars[targetContainerID]
	return sidecarID, exists
}

// ListSidecars returns a snapshot copy of all created sidecars. The copy is
// safe to iterate even if other goroutines mutate the backing map
// concurrently (e.g. CleanupAll destroys in a loop while emergency-stop
// fires another DestroySidecar). Callers that need to observe live state
// should call this again rather than hold the returned map across ops.
func (m *Manager) ListSidecars() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.createdSidecars))
	for target, sidecar := range m.createdSidecars {
		result[target] = sidecar
	}
	return result
}

// destroyOrphanSidecar is used to clean up a just-created sidecar that
// lost a CreateSidecar race. It runs in a fresh background context so the
// caller's ctx cancellation does not prevent the cleanup, and it
// intentionally ignores errors — the duplicate is not tracked anywhere,
// so any residual container becomes a next-run pre-flight sweep problem
// rather than a hot-path failure.
func (m *Manager) destroyOrphanSidecar(sidecarID string) {
	ctx := context.Background()
	timeout := 10
	if err := m.dockerClient.ContainerStop(ctx, sidecarID, &timeout); err != nil {
		if !errdefs.IsNotFound(err) && !errdefs.IsNotModified(err) {
			fmt.Printf("Orphan sidecar stop failed for %s: %v\n", sidecarID[:12], err)
		}
	}
	if err := m.dockerClient.ContainerRemove(ctx, sidecarID, types.ContainerRemoveOptions{Force: true}); err != nil {
		if !errdefs.IsNotFound(err) && !errdefs.IsConflict(err) {
			fmt.Printf("Orphan sidecar remove failed for %s: %v\n", sidecarID[:12], err)
		}
	}
}
