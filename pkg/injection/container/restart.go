package container

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// RestartManager handles container restart operations
type RestartManager struct {
	dockerClient *client.Client
}

// NewRestartManager creates a new RestartManager
func NewRestartManager(dockerClient *client.Client) *RestartManager {
	return &RestartManager{
		dockerClient: dockerClient,
	}
}

// RestartContainer restarts a single container with the given parameters
func (rm *RestartManager) RestartContainer(ctx context.Context, containerID string, params RestartParams) error {
	log.Info().
		Str("container", containerID).
		Int("grace_period", params.GracePeriod).
		Int("restart_delay", params.RestartDelay).
		Msg("Restarting container")

	// Default grace period is 10 seconds
	gracePeriod := params.GracePeriod
	if gracePeriod == 0 {
		gracePeriod = 10
	}

	// 1. Stop container with grace period
	stopOptions := container.StopOptions{
		Timeout: func() *int { t := gracePeriod; return &t }(),
	}

	if err := rm.dockerClient.ContainerStop(ctx, containerID, stopOptions); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", containerID, err)
	}

	log.Debug().Str("container", containerID).Msg("Container stopped")

	// 2. Wait for container to fully stop
	if err := rm.waitForStop(ctx, containerID, 30*time.Second); err != nil {
		return fmt.Errorf("container %s did not stop in time: %w", containerID, err)
	}

	// 3. Optional delay before restart
	if params.RestartDelay > 0 {
		log.Debug().
			Str("container", containerID).
			Int("delay_seconds", params.RestartDelay).
			Msg("Waiting before restart")
		time.Sleep(time.Duration(params.RestartDelay) * time.Second)
	}

	// 4. Start container
	if err := rm.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", containerID, err)
	}

	log.Info().Str("container", containerID).Msg("Container restarted successfully")

	// 5. Wait for container to be running (validators need time to initialize)
	if err := rm.waitForRunning(ctx, containerID, 120*time.Second); err != nil {
		return fmt.Errorf("container %s did not start in time: %w", containerID, err)
	}

	return nil
}

// RestartContainersSimultaneous restarts multiple containers simultaneously
// All containers are stopped first, then all are started (truly simultaneous downtime)
func (rm *RestartManager) RestartContainersSimultaneous(ctx context.Context, containerIDs []string, params RestartParams) error {
	log.Info().
		Int("count", len(containerIDs)).
		Msg("Restarting containers simultaneously")

	gracePeriod := params.GracePeriod
	if gracePeriod == 0 {
		gracePeriod = 10
	}

	stopOptions := container.StopOptions{
		Timeout: func() *int { t := gracePeriod; return &t }(),
	}

	// Phase 1: Stop all containers
	log.Debug().Msg("Phase 1: Stopping all containers")
	for i, containerID := range containerIDs {
		if err := rm.dockerClient.ContainerStop(ctx, containerID, stopOptions); err != nil {
			return fmt.Errorf("failed to stop container %d/%d: %w", i+1, len(containerIDs), err)
		}
		log.Debug().Str("container", containerID).Int("index", i+1).Msg("Container stop initiated")
	}

	// Phase 2: Wait for all containers to stop
	log.Debug().Msg("Phase 2: Waiting for all containers to stop")
	for i, containerID := range containerIDs {
		if err := rm.waitForStop(ctx, containerID, 30*time.Second); err != nil {
			return fmt.Errorf("container %d/%d did not stop in time: %w", i+1, len(containerIDs), err)
		}
		log.Debug().Str("container", containerID).Int("index", i+1).Msg("Container stopped")
	}

	// Phase 3: Optional delay before restart
	if params.RestartDelay > 0 {
		log.Debug().Int("delay_seconds", params.RestartDelay).Msg("Phase 3: Waiting before restart")
		time.Sleep(time.Duration(params.RestartDelay) * time.Second)
	}

	// Phase 4: Start all containers
	log.Debug().Msg("Phase 4: Starting all containers")
	for i, containerID := range containerIDs {
		if err := rm.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start container %d/%d: %w", i+1, len(containerIDs), err)
		}
		log.Debug().Str("container", containerID).Int("index", i+1).Msg("Container start initiated")
	}

	// Phase 5: Wait for all containers to be running
	log.Debug().Msg("Phase 5: Waiting for all containers to be running")
	for i, containerID := range containerIDs {
		if err := rm.waitForRunning(ctx, containerID, 120*time.Second); err != nil {
			return fmt.Errorf("container %d/%d did not start in time: %w", i+1, len(containerIDs), err)
		}
		log.Debug().Str("container", containerID).Int("index", i+1).Msg("Container running")
	}

	log.Info().Int("count", len(containerIDs)).Msg("All containers restarted simultaneously")
	return nil
}

// RestartContainersStaggered restarts multiple containers with a stagger delay between each
func (rm *RestartManager) RestartContainersStaggered(ctx context.Context, containerIDs []string, params RestartParams) error {
	log.Info().
		Int("count", len(containerIDs)).
		Int("stagger_seconds", params.Stagger).
		Msg("Restarting containers with stagger")

	for i, containerID := range containerIDs {
		// Restart this container
		if err := rm.RestartContainer(ctx, containerID, params); err != nil {
			return fmt.Errorf("failed to restart container %d/%d: %w", i+1, len(containerIDs), err)
		}

		// Wait stagger duration between restarts (except after last)
		if i < len(containerIDs)-1 && params.Stagger > 0 {
			log.Debug().
				Int("current", i+1).
				Int("total", len(containerIDs)).
				Int("stagger_seconds", params.Stagger).
				Msg("Waiting before next restart")
			time.Sleep(time.Duration(params.Stagger) * time.Second)
		}
	}

	log.Info().Int("count", len(containerIDs)).Msg("All containers restarted successfully")
	return nil
}

// waitForStop waits for a container to stop
func (rm *RestartManager) waitForStop(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		inspect, err := rm.dockerClient.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to inspect container: %w", err)
		}

		if !inspect.State.Running {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("container did not stop within %v", timeout)
}

// waitForRunning waits for a container to start running
func (rm *RestartManager) waitForRunning(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		inspect, err := rm.dockerClient.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to inspect container: %w", err)
		}

		if inspect.State.Running {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("container did not start within %v", timeout)
}
