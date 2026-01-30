package container

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// KillManager handles container kill operations
type KillManager struct {
	dockerClient *client.Client
	restartMgr   *RestartManager
}

// NewKillManager creates a new KillManager
func NewKillManager(dockerClient *client.Client) *KillManager {
	return &KillManager{
		dockerClient: dockerClient,
		restartMgr:   NewRestartManager(dockerClient),
	}
}

// KillContainer kills a container with the specified signal
func (km *KillManager) KillContainer(ctx context.Context, containerID string, params KillParams) error {
	signal := params.Signal
	if signal == "" {
		signal = "SIGKILL"
	}

	log.Info().
		Str("container", containerID).
		Str("signal", signal).
		Bool("restart", params.Restart).
		Msg("Killing container")

	// 1. Kill container with specified signal
	if err := km.dockerClient.ContainerKill(ctx, containerID, signal); err != nil {
		return fmt.Errorf("failed to kill container %s: %w", containerID, err)
	}

	log.Debug().Str("container", containerID).Msg("Kill signal sent")

	// 2. Wait for container to stop
	if err := km.restartMgr.waitForStop(ctx, containerID, 30*time.Second); err != nil {
		// Container might already be stopped, which is fine
		log.Warn().Err(err).Str("container", containerID).Msg("Container state check after kill")
	}

	// 3. Optionally restart after delay
	if params.Restart {
		if params.RestartDelay > 0 {
			log.Debug().
				Str("container", containerID).
				Int("delay_seconds", params.RestartDelay).
				Msg("Waiting before restart")
			time.Sleep(time.Duration(params.RestartDelay) * time.Second)
		}

		log.Info().Str("container", containerID).Msg("Restarting killed container")
		if err := km.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to restart container %s: %w", containerID, err)
		}

		// Wait for container to start
		if err := km.restartMgr.waitForRunning(ctx, containerID, 30*time.Second); err != nil {
			return fmt.Errorf("container %s did not restart in time: %w", containerID, err)
		}

		log.Info().Str("container", containerID).Msg("Container restarted after kill")
	}

	return nil
}
