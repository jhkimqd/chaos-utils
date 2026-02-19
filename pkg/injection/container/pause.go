package container

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// PauseManager handles container pause/unpause operations
type PauseManager struct {
	dockerClient     *client.Client
	mu               sync.Mutex
	pausedContainers map[string]bool // Track paused containers for cleanup
}

// NewPauseManager creates a new PauseManager
func NewPauseManager(dockerClient *client.Client) *PauseManager {
	return &PauseManager{
		dockerClient:     dockerClient,
		pausedContainers: make(map[string]bool),
	}
}

// PauseContainer pauses a container for the specified duration
func (pm *PauseManager) PauseContainer(ctx context.Context, containerID string, params PauseParams) error {
	log.Info().
		Str("container", containerID).
		Dur("duration", params.Duration).
		Msg("Pausing container")

	// 1. Pause container (sends SIGSTOP)
	if err := pm.dockerClient.ContainerPause(ctx, containerID); err != nil {
		return fmt.Errorf("failed to pause container %s: %w", containerID, err)
	}

	// Track this container as paused
	pm.mu.Lock()
	pm.pausedContainers[containerID] = true
	pm.mu.Unlock()

	log.Info().Str("container", containerID).Msg("Container paused")

	// 2. Wait for specified duration
	if params.Duration > 0 {
		log.Debug().
			Str("container", containerID).
			Dur("duration", params.Duration).
			Msg("Container will remain paused")

		select {
		case <-time.After(params.Duration):
			// Duration elapsed, continue to unpause
		case <-ctx.Done():
			// Context cancelled, unpause and return
			log.Warn().Str("container", containerID).Msg("Context cancelled during pause")
			if err := pm.UnpauseContainer(ctx, containerID); err != nil {
				log.Error().Err(err).Str("container", containerID).Msg("Failed to unpause after cancellation")
			}
			return ctx.Err()
		}
	}

	// 3. Unpause if configured to do so
	if params.Unpause || params.Duration > 0 {
		return pm.UnpauseContainer(ctx, containerID)
	}

	return nil
}

// UnpauseContainer unpauses a container
func (pm *PauseManager) UnpauseContainer(ctx context.Context, containerID string) error {
	log.Info().Str("container", containerID).Msg("Unpausing container")

	if err := pm.dockerClient.ContainerUnpause(ctx, containerID); err != nil {
		return fmt.Errorf("failed to unpause container %s: %w", containerID, err)
	}

	// Remove from paused tracking
	pm.mu.Lock()
	delete(pm.pausedContainers, containerID)
	pm.mu.Unlock()

	log.Info().Str("container", containerID).Msg("Container unpaused")
	return nil
}

// CleanupAllPaused unpauses all tracked paused containers (for emergency cleanup)
func (pm *PauseManager) CleanupAllPaused(ctx context.Context) error {
	pm.mu.Lock()
	ids := make([]string, 0, len(pm.pausedContainers))
	for id := range pm.pausedContainers {
		ids = append(ids, id)
	}
	pm.mu.Unlock()

	if len(ids) == 0 {
		return nil
	}

	log.Info().Int("count", len(ids)).Msg("Unpausing all paused containers")

	var lastErr error
	for _, containerID := range ids {
		if err := pm.UnpauseContainer(ctx, containerID); err != nil {
			log.Error().Err(err).Str("container", containerID).Msg("Failed to unpause container")
			lastErr = err
		}
	}

	return lastErr
}

// GetPausedContainers returns the list of currently paused containers
func (pm *PauseManager) GetPausedContainers() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	containers := make([]string, 0, len(pm.pausedContainers))
	for containerID := range pm.pausedContainers {
		containers = append(containers, containerID)
	}
	return containers
}
