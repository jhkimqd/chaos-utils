package injection

import (
	"context"
	"fmt"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/container"
	"github.com/jihwankim/chaos-utils/pkg/injection/l3l4"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
	"github.com/jihwankim/chaos-utils/pkg/injection/stress"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// Target represents a fault injection target
type Target struct {
	Name        string
	ContainerID string
}

// Injector provides unified interface for all fault types
type Injector struct {
	networkInjector  *l3l4.ComcastWrapper
	containerManager *container.Manager
	stressInjector   *stress.StressWrapper
}

// New creates a new unified fault injector
func New(sidecarMgr *sidecar.Manager, dockerClient *docker.Client) *Injector {
	return &Injector{
		networkInjector:  l3l4.New(sidecarMgr),
		containerManager: container.NewManager(dockerClient.GetClient()),
		stressInjector:   stress.New(sidecarMgr, dockerClient),
	}
}

// InjectFault injects a fault based on its type
func (i *Injector) InjectFault(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	switch fault.Type {
	case "network":
		return i.injectNetworkFault(ctx, fault, targets)
	case "container_restart":
		return i.injectContainerRestart(ctx, fault, targets)
	case "container_kill":
		return i.injectContainerKill(ctx, fault, targets)
	case "container_pause":
		return i.injectContainerPause(ctx, fault, targets)
	case "cpu_stress", "cpu":
		return i.injectCPUStress(ctx, fault, targets)
	case "memory_stress", "memory_pressure", "memory":
		return i.injectMemoryStress(ctx, fault, targets)
	default:
		return fmt.Errorf("unknown fault type: %s", fault.Type)
	}
}

// injectNetworkFault handles network fault injection
func (i *Injector) injectNetworkFault(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse network fault parameters
	params := l3l4.FaultParams{
		Device: "eth0", // default device
	}

	if fault.Params != nil {
		if device, ok := fault.Params["device"].(string); ok {
			params.Device = device
		}
		if latency, ok := fault.Params["latency"].(int); ok {
			params.Latency = latency
		}
		if packetLoss, ok := fault.Params["packet_loss"].(float64); ok {
			params.PacketLoss = packetLoss
		} else if packetLoss, ok := fault.Params["packet_loss"].(int); ok {
			params.PacketLoss = float64(packetLoss)
		}
		if bandwidth, ok := fault.Params["bandwidth"].(int); ok {
			params.Bandwidth = bandwidth
		}
		if targetPorts, ok := fault.Params["target_ports"].(string); ok {
			params.TargetPorts = targetPorts
		}
		if targetProto, ok := fault.Params["target_proto"].(string); ok {
			params.TargetProto = targetProto
		}
		if targetIPs, ok := fault.Params["target_ips"].(string); ok {
			params.TargetIPs = targetIPs
		}
		if targetCIDR, ok := fault.Params["target_cidr"].(string); ok {
			params.TargetCIDR = targetCIDR
		}
	}

	// Inject on all targets
	for _, target := range targets {
		if err := i.networkInjector.InjectFault(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject network fault on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectContainerRestart handles container restart faults
func (i *Injector) injectContainerRestart(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse restart parameters
	params := container.RestartParams{
		GracePeriod:  10, // default
		RestartDelay: 0,
		Stagger:      0,
	}

	if fault.Params != nil {
		if gracePeriod, ok := fault.Params["grace_period"].(int); ok {
			params.GracePeriod = gracePeriod
		}
		if restartDelay, ok := fault.Params["restart_delay"].(int); ok {
			params.RestartDelay = restartDelay
		}
		if stagger, ok := fault.Params["stagger"].(int); ok {
			params.Stagger = stagger
		}
	}

	// Collect all container IDs
	containerIDs := make([]string, len(targets))
	for i, target := range targets {
		containerIDs[i] = target.ContainerID
	}

	// If stagger is specified and we have multiple targets, use staggered restart
	if params.Stagger > 0 && len(containerIDs) > 1 {
		return i.containerManager.RestartContainersStaggered(ctx, containerIDs, params)
	}

	// Otherwise restart all simultaneously
	for _, containerID := range containerIDs {
		if err := i.containerManager.RestartContainer(ctx, containerID, params); err != nil {
			return fmt.Errorf("failed to restart container %s: %w", containerID, err)
		}
	}

	return nil
}

// injectContainerKill handles container kill faults
func (i *Injector) injectContainerKill(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse kill parameters
	params := container.KillParams{
		Signal:       "SIGKILL", // default
		Restart:      true,
		RestartDelay: 0,
	}

	if fault.Params != nil {
		if signal, ok := fault.Params["signal"].(string); ok {
			params.Signal = signal
		}
		if restart, ok := fault.Params["restart"].(bool); ok {
			params.Restart = restart
		}
		if restartDelay, ok := fault.Params["restart_delay"].(int); ok {
			params.RestartDelay = restartDelay
		}
	}

	// Kill all targets
	for _, target := range targets {
		if err := i.containerManager.KillContainer(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to kill container %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectContainerPause handles container pause faults
func (i *Injector) injectContainerPause(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse pause parameters
	params := container.PauseParams{
		Duration: 0,
		Unpause:  true, // default
	}

	if fault.Params != nil {
		if durationStr, ok := fault.Params["duration"].(string); ok {
			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				return fmt.Errorf("invalid duration format: %w", err)
			}
			params.Duration = duration
		}
		if unpause, ok := fault.Params["unpause"].(bool); ok {
			params.Unpause = unpause
		}
	}

	// Pause all targets
	for _, target := range targets {
		if err := i.containerManager.PauseContainer(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to pause container %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectCPUStress handles CPU stress injection
func (i *Injector) injectCPUStress(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse CPU stress parameters
	params := stress.StressParams{
		Method:     "stress", // default
		CPUPercent: 50,       // default
		Duration:   "5m",     // default
		Cores:      1,        // default - limiting to 50% of 1 core = 0.5 cores
	}

	if fault.Params != nil {
		if method, ok := fault.Params["method"].(string); ok {
			params.Method = method
		}
		if cpuPercent, ok := fault.Params["cpu_percent"].(int); ok {
			params.CPUPercent = cpuPercent
		} else if cpuPercent, ok := fault.Params["cpu_percent"].(float64); ok {
			params.CPUPercent = int(cpuPercent)
		}
		if duration, ok := fault.Params["duration"].(string); ok {
			params.Duration = duration
		}
		if cores, ok := fault.Params["cores"].(int); ok {
			params.Cores = cores
		} else if cores, ok := fault.Params["cores"].(float64); ok {
			params.Cores = int(cores)
		}
	}

	// Validate parameters
	if err := stress.ValidateStressParams(params); err != nil {
		return fmt.Errorf("invalid CPU stress parameters: %w", err)
	}

	// Inject on all targets
	for _, target := range targets {
		if err := i.stressInjector.InjectCPUStress(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject CPU stress on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectMemoryStress handles memory stress injection
func (i *Injector) injectMemoryStress(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse memory stress parameters
	params := stress.StressParams{
		Method:   "stress",
		MemoryMB: 512, // default
		Duration: "5m",
	}

	if fault.Params != nil {
		if method, ok := fault.Params["method"].(string); ok {
			params.Method = method
		}
		if memoryMB, ok := fault.Params["memory_mb"].(int); ok {
			params.MemoryMB = memoryMB
		} else if memoryMB, ok := fault.Params["memory_mb"].(float64); ok {
			params.MemoryMB = int(memoryMB)
		}
		if duration, ok := fault.Params["duration"].(string); ok {
			params.Duration = duration
		}
	}

	// Validate parameters
	if err := stress.ValidateStressParams(params); err != nil {
		return fmt.Errorf("invalid memory stress parameters: %w", err)
	}

	// Inject on all targets
	for _, target := range targets {
		if err := i.stressInjector.InjectMemoryStress(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject memory stress on %s: %w", target.Name, err)
		}
	}

	return nil
}

// RemoveFault removes a fault from a target
func (i *Injector) RemoveFault(ctx context.Context, faultType string, containerID string) error {
	switch faultType {
	case "network":
		return i.networkInjector.RemoveFault(ctx, containerID)
	case "container_restart", "container_kill":
		// Restart and kill don't need removal - containers are already running
		return nil
	case "container_pause":
		// Unpause if it was paused
		return i.containerManager.UnpauseContainer(ctx, containerID)
	case "cpu_stress", "cpu", "memory_stress", "memory_pressure", "memory":
		// Remove stress faults
		return i.stressInjector.RemoveFault(ctx, containerID)
	default:
		return fmt.Errorf("unknown fault type for removal: %s", faultType)
	}
}

// Cleanup performs emergency cleanup of all faults
func (i *Injector) Cleanup(ctx context.Context) error {
	// Cleanup container faults
	if err := i.containerManager.Cleanup(ctx); err != nil {
		return fmt.Errorf("failed to cleanup container faults: %w", err)
	}

	// Network faults are cleaned up by the orchestrator via sidecar removal
	return nil
}
