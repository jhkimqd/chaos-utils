package container

import (
	"context"

	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// Manager provides unified interface for all container lifecycle operations
type Manager struct {
	dockerClient *client.Client
	restartMgr   *RestartManager
	killMgr      *KillManager
	pauseMgr     *PauseManager
}

// NewManager creates a new container Manager
func NewManager(dockerClient *client.Client) *Manager {
	return &Manager{
		dockerClient: dockerClient,
		restartMgr:   NewRestartManager(dockerClient),
		killMgr:      NewKillManager(dockerClient),
		pauseMgr:     NewPauseManager(dockerClient),
	}
}

// RestartContainer restarts a container
func (m *Manager) RestartContainer(ctx context.Context, containerID string, params RestartParams) error {
	return m.restartMgr.RestartContainer(ctx, containerID, params)
}

// RestartContainersStaggered restarts multiple containers with stagger
func (m *Manager) RestartContainersStaggered(ctx context.Context, containerIDs []string, params RestartParams) error {
	return m.restartMgr.RestartContainersStaggered(ctx, containerIDs, params)
}

// KillContainer kills a container
func (m *Manager) KillContainer(ctx context.Context, containerID string, params KillParams) error {
	return m.killMgr.KillContainer(ctx, containerID, params)
}

// PauseContainer pauses a container
func (m *Manager) PauseContainer(ctx context.Context, containerID string, params PauseParams) error {
	return m.pauseMgr.PauseContainer(ctx, containerID, params)
}

// UnpauseContainer unpauses a container
func (m *Manager) UnpauseContainer(ctx context.Context, containerID string) error {
	return m.pauseMgr.UnpauseContainer(ctx, containerID)
}

// Cleanup performs emergency cleanup of all container faults
func (m *Manager) Cleanup(ctx context.Context) error {
	log.Info().Msg("Cleaning up container lifecycle faults")

	// Unpause any paused containers
	if err := m.pauseMgr.CleanupAllPaused(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to cleanup paused containers")
		return err
	}

	// Note: Restarted and killed containers don't need cleanup
	// as they're already in running state (or will be restarted)

	return nil
}
