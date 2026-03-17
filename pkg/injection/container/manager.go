package container

import (
	"context"

	"github.com/docker/docker/client"
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

// RestartContainersSimultaneous restarts multiple containers simultaneously
func (m *Manager) RestartContainersSimultaneous(ctx context.Context, containerIDs []string, params RestartParams) error {
	return m.restartMgr.RestartContainersSimultaneous(ctx, containerIDs, params)
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

