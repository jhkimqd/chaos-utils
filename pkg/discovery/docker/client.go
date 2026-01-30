package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/jihwankim/chaos-utils/pkg/discovery"
)

// Client wraps Docker API client for service discovery and container management
type Client struct {
	cli *client.Client
}

// New creates a new Docker client
func New() (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Client{cli: cli}, nil
}

// Close closes the Docker client connection
func (c *Client) Close() error {
	if c.cli != nil {
		return c.cli.Close()
	}
	return nil
}

// GetClient returns the underlying Docker API client
func (c *Client) GetClient() *client.Client {
	return c.cli
}

// GetContainerByName finds a container by name
func (c *Client) GetContainerByName(ctx context.Context, name string) (*discovery.Service, error) {
	containers, err := c.cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	for _, ctr := range containers {
		for _, ctrName := range ctr.Names {
			// Docker adds '/' prefix to container names
			if ctrName == "/"+name || ctrName == name {
				return c.containerToService(ctx, ctr)
			}
		}
	}

	return nil, fmt.Errorf("container not found: %s", name)
}

// GetContainerByID finds a container by ID
func (c *Client) GetContainerByID(ctx context.Context, id string) (*discovery.Service, error) {
	ctr, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return c.inspectToService(ctr)
}

// GetContainersByLabel finds containers matching label filters
func (c *Client) GetContainersByLabel(ctx context.Context, labels map[string]string) ([]*discovery.Service, error) {
	f := buildLabelFilters(labels)

	containers, err := c.cli.ContainerList(ctx, types.ContainerListOptions{
		Filters: f,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	services := make([]*discovery.Service, 0, len(containers))
	for _, ctr := range containers {
		svc, err := c.containerToService(ctx, ctr)
		if err != nil {
			// Log warning but continue
			fmt.Printf("Warning: failed to convert container %s: %v\n", ctr.ID[:12], err)
			continue
		}
		services = append(services, svc)
	}

	return services, nil
}

// GetContainerPID gets the PID of a container
func (c *Client) GetContainerPID(ctx context.Context, containerID string) (int, error) {
	ctr, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, fmt.Errorf("failed to inspect container: %w", err)
	}

	return ctr.State.Pid, nil
}

// ExecCommand executes a command in a container and returns output
func (c *Client) ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error) {
	// Create exec instance
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execID, err := c.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec: %w", err)
	}

	// Attach to exec instance
	resp, err := c.cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return "", fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer resp.Close()

	// Read output
	output, err := io.ReadAll(resp.Reader)
	if err != nil {
		return string(output), fmt.Errorf("failed to read output: %w", err)
	}

	// Check exit code
	inspectResp, err := c.cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return string(output), fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspectResp.ExitCode != 0 {
		return string(output), fmt.Errorf("command exited with code %d: %s", inspectResp.ExitCode, string(output))
	}

	return string(output), nil
}

// Helper function to convert types.Container to Service
func (c *Client) containerToService(ctx context.Context, ctr types.Container) (*discovery.Service, error) {
	// Get full container details
	inspectData, err := c.cli.ContainerInspect(ctx, ctr.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return c.inspectToService(inspectData)
}

// Helper function to convert inspect data to Service
func (c *Client) inspectToService(ctr types.ContainerJSON) (*discovery.Service, error) {
	svc := &discovery.Service{
		ContainerID:   ctr.ID[:12], // Short ID
		ContainerName: ctr.Name,
		NetworkMode:   string(ctr.HostConfig.NetworkMode),
		PID:           ctr.State.Pid,
		Labels:        ctr.Config.Labels,
		Ports:         make(map[string]uint16),
	}

	// Extract name (remove leading '/')
	if len(ctr.Name) > 0 && ctr.Name[0] == '/' {
		svc.Name = ctr.Name[1:]
	} else {
		svc.Name = ctr.Name
	}

	// Get IP address (try to get from first network)
	if len(ctr.NetworkSettings.Networks) > 0 {
		for _, network := range ctr.NetworkSettings.Networks {
			svc.IP = network.IPAddress
			break
		}
	}

	// Extract ports
	for port, bindings := range ctr.NetworkSettings.Ports {
		if len(bindings) > 0 {
			portNum, err := strconv.Atoi(bindings[0].HostPort)
			if err == nil {
				svc.Ports[string(port)] = uint16(portNum)
			}
		}
	}

	return svc, nil
}

// ContainerCreate creates a new container
func (c *Client) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
	return c.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

// ContainerStart starts a container
func (c *Client) ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error {
	return c.cli.ContainerStart(ctx, containerID, options)
}

// ContainerStop stops a container
func (c *Client) ContainerStop(ctx context.Context, containerID string, timeout *int) error {
	var options container.StopOptions
	if timeout != nil {
		options.Timeout = timeout
	}
	return c.cli.ContainerStop(ctx, containerID, options)
}

// ContainerRemove removes a container
func (c *Client) ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error {
	return c.cli.ContainerRemove(ctx, containerID, options)
}

// ContainerList lists all containers
func (c *Client) ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error) {
	return c.cli.ContainerList(ctx, options)
}

// ContainerInspect returns detailed information about a container
func (c *Client) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	return c.cli.ContainerInspect(ctx, containerID)
}

// ContainerUpdate updates container configuration
func (c *Client) ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error) {
	return c.cli.ContainerUpdate(ctx, containerID, updateConfig)
}

// Helper to build Docker API filters from label map
func buildLabelFilters(labels map[string]string) filters.Args {
	f := filters.NewArgs()
	for key, value := range labels {
		if value == "" {
			f.Add("label", key)
		} else {
			f.Add("label", fmt.Sprintf("%s=%s", key, value))
		}
	}
	return f
}
