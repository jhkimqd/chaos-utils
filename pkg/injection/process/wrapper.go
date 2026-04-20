package process

import (
	"context"
)

// Wrapper wraps process-targeted fault injection (kill).
type Wrapper struct {
	dockerClient DockerClient
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// New creates a new process wrapper
func New(dockerClient DockerClient) *Wrapper {
	return &Wrapper{
		dockerClient: dockerClient,
	}
}
