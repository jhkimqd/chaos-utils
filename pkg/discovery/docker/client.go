package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
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

// GetContainerByID finds a container by ID
func (c *Client) GetContainerByID(ctx context.Context, id string) (*discovery.Service, error) {
	ctr, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return c.inspectToService(ctr)
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

	// Docker exec streams are multiplexed (8-byte header per chunk) when
	// no TTY is allocated. Use stdcopy.StdCopy to demultiplex into clean output.
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		return stdout.String(), fmt.Errorf("failed to read output: %w", err)
	}

	// Check exit code
	inspectResp, err := c.cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return stdout.String(), fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspectResp.ExitCode != 0 {
		combined := stdout.String() + stderr.String()
		return combined, fmt.Errorf("command exited with code %d: %s", inspectResp.ExitCode, combined)
	}

	return stdout.String(), nil
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

// EnsureImage checks if an image exists locally and pulls it if not.
// Returns an error if the image cannot be found or pulled.
func (c *Client) EnsureImage(ctx context.Context, image string) error {
	_, _, err := c.cli.ImageInspectWithRaw(ctx, image)
	if err == nil {
		return nil // image exists locally
	}

	fmt.Printf("Image %s not found locally, pulling...\n", image)
	reader, pullErr := c.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if pullErr != nil {
		return fmt.Errorf("image %s not found locally and pull failed: %w", image, pullErr)
	}
	defer reader.Close()
	// Drain the pull progress stream — ImagePull is not complete until EOF.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("error reading pull response for %s: %w", image, err)
	}
	return nil
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

// ContainerLogs fetches the last tailN log lines from a container since the given
// time, merging stdout and stderr. Returns an empty slice on any error — callers
// should treat log collection as best-effort and never fail on it.
func (c *Client) ContainerLogs(ctx context.Context, containerID string, tailN int, since time.Time) ([]string, error) {
	opts := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tailN),
		Since:      since.UTC().Format(time.RFC3339),
	}
	reader, err := c.cli.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		return nil, fmt.Errorf("container logs: %w", err)
	}
	defer reader.Close()

	// Docker container log streams are multiplexed (8-byte header per chunk).
	// stdcopy.StdCopy demultiplexes stdout and stderr into separate buffers.
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, reader); err != nil {
		return nil, fmt.Errorf("demux container logs: %w", err)
	}

	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// ContainerLogsFollow returns a streaming reader that follows container logs in
// real-time starting from the given time. The caller must close the returned
// ReadCloser when done. The stream is multiplexed (8-byte Docker header per
// chunk) — use stdcopy.StdCopy to demultiplex.
func (c *Client) ContainerLogsFollow(ctx context.Context, containerID string, since time.Time) (io.ReadCloser, error) {
	opts := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Since:      since.UTC().Format(time.RFC3339),
	}
	reader, err := c.cli.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		return nil, fmt.Errorf("container logs follow: %w", err)
	}
	return reader, nil
}

// ContainerInspect returns detailed information about a container
func (c *Client) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	return c.cli.ContainerInspect(ctx, containerID)
}

// ContainerUpdate updates container configuration
func (c *Client) ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error) {
	return c.cli.ContainerUpdate(ctx, containerID, updateConfig)
}

