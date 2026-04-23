package injection

import (
	"context"
	"fmt"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/container"
	"github.com/jihwankim/chaos-utils/pkg/injection/disk"
	"github.com/jihwankim/chaos-utils/pkg/injection/dns"
	"github.com/jihwankim/chaos-utils/pkg/injection/firewall"
	"github.com/jihwankim/chaos-utils/pkg/injection/l3l4"
	"github.com/jihwankim/chaos-utils/pkg/injection/process"
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
	tcInjector       *l3l4.TCWrapper
	containerManager *container.Manager
	stressInjector   *stress.StressWrapper
	firewallInjector *firewall.IptablesWrapper
	dnsInjector      *dns.DNSWrapper
	processInjector  *process.PriorityWrapper
	diskInjector     *disk.IODelayWrapper
}

// New creates a new unified fault injector
func New(sidecarMgr *sidecar.Manager, dockerClient *docker.Client) *Injector {
	return &Injector{
		networkInjector:  l3l4.New(sidecarMgr),
		tcInjector:       l3l4.NewTCWrapper(sidecarMgr),
		containerManager: container.NewManager(dockerClient.GetClient()),
		stressInjector:   stress.New(dockerClient),
		firewallInjector: firewall.New(sidecarMgr),
		dnsInjector:      dns.New(sidecarMgr),
		processInjector:  process.New(dockerClient),
		diskInjector:     disk.New(dockerClient),
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
	case "connection_drop":
		return i.injectConnectionDrop(ctx, fault, targets)
	case "dns":
		return i.injectDNSDelay(ctx, fault, targets)
	case "process_priority":
		return i.injectProcessPriority(ctx, fault, targets)
	case "disk_io":
		return i.injectDiskIODelay(ctx, fault, targets)
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
		if reorder, ok := fault.Params["reorder"].(int); ok {
			params.Reorder = reorder
		}
		if reorderCorr, ok := fault.Params["reorder_correlation"].(int); ok {
			params.ReorderCorrelation = reorderCorr
		}
	}

	// If packet reordering is specified, use TC wrapper instead of comcast
	if params.Reorder > 0 {
		if params.Latency == 0 {
			return fmt.Errorf("packet reordering requires latency to be set")
		}
		for _, target := range targets {
			if err := i.tcInjector.InjectPacketReorder(ctx, target.ContainerID, params); err != nil {
				return fmt.Errorf("failed to inject packet reorder on %s: %w", target.Name, err)
			}
		}
		return nil
	}

	// Otherwise use comcast for standard network faults
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

	// Choose restart strategy based on parameters
	if len(containerIDs) == 1 {
		// Single container - use simple restart
		return i.containerManager.RestartContainer(ctx, containerIDs[0], params)
	}

	if params.Stagger > 0 {
		// Multiple containers with stagger - restart one by one with delay
		return i.containerManager.RestartContainersStaggered(ctx, containerIDs, params)
	}

	// Multiple containers with stagger=0 - truly simultaneous restart
	// Stop all first, then start all
	return i.containerManager.RestartContainersSimultaneous(ctx, containerIDs, params)
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

// injectConnectionDrop handles connection drop fault injection
func (i *Injector) injectConnectionDrop(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := firewall.ConnectionDropParams{
		RuleType:    "drop",
		TargetProto: "tcp",
		Probability: 0.1,
		Stateful:    true,
	}

	if fault.Params != nil {
		if ruleType, ok := fault.Params["rule_type"].(string); ok {
			params.RuleType = ruleType
		}
		if targetPorts, ok := fault.Params["target_ports"].(string); ok {
			params.TargetPorts = targetPorts
		}
		if targetProto, ok := fault.Params["target_proto"].(string); ok {
			params.TargetProto = targetProto
		}
		if prob, ok := fault.Params["probability"].(float64); ok {
			params.Probability = prob
		}
		if stateful, ok := fault.Params["stateful"].(bool); ok {
			params.Stateful = stateful
		}
	}

	if err := firewall.ValidateConnectionDropParams(params); err != nil {
		return fmt.Errorf("invalid connection drop parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.firewallInjector.InjectConnectionDrop(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject connection drop on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectDNSDelay handles DNS delay fault injection
func (i *Injector) injectDNSDelay(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := dns.DNSParams{
		DelayMs:     2000,
		FailureRate: 0,
		Method:      "dnsmasq",
	}

	if fault.Params != nil {
		if delayMs, ok := fault.Params["delay_ms"].(int); ok {
			params.DelayMs = delayMs
		}
		if failureRate, ok := fault.Params["failure_rate"].(float64); ok {
			params.FailureRate = failureRate
		}
		if method, ok := fault.Params["method"].(string); ok {
			params.Method = method
		}
	}

	if err := dns.ValidateDNSParams(params); err != nil {
		return fmt.Errorf("invalid DNS parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.dnsInjector.InjectDNSDelay(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject DNS delay on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectProcessPriority handles process priority fault injection
func (i *Injector) injectProcessPriority(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := process.PriorityParams{
		Priority: 19,
	}

	if fault.Params != nil {
		if priority, ok := fault.Params["priority"].(int); ok {
			params.Priority = priority
		}
		if processPattern, ok := fault.Params["process_pattern"].(string); ok {
			params.ProcessPattern = processPattern
		}
	}

	if err := process.ValidatePriorityParams(params); err != nil {
		return fmt.Errorf("invalid priority parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.processInjector.InjectPriorityChange(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject priority change on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectDiskIODelay handles disk I/O delay fault injection
func (i *Injector) injectDiskIODelay(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := disk.IODelayParams{
		IOLatencyMs: 200,
		Operation:   "all",
		Method:      "dm-delay",
	}

	if fault.Params != nil {
		if ioLatencyMs, ok := fault.Params["io_latency_ms"].(int); ok {
			params.IOLatencyMs = ioLatencyMs
		}
		if targetPath, ok := fault.Params["target_path"].(string); ok {
			params.TargetPath = targetPath
		}
		if operation, ok := fault.Params["operation"].(string); ok {
			params.Operation = operation
		}
		if method, ok := fault.Params["method"].(string); ok {
			params.Method = method
		}
	}

	if err := disk.ValidateIODelayParams(params); err != nil {
		return fmt.Errorf("invalid I/O delay parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.diskInjector.InjectIODelay(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject I/O delay on %s: %w", target.Name, err)
		}
	}

	return nil
}

// RemoveFault removes a fault from a target
func (i *Injector) RemoveFault(ctx context.Context, faultType string, containerID string) error {
	switch faultType {
	case "network":
		// Try to remove both comcast and tc rules
		_ = i.networkInjector.RemoveFault(ctx, containerID)
		_ = i.tcInjector.RemoveFault(ctx, containerID)
		return nil
	case "container_restart", "container_kill":
		// Restart and kill don't need removal - containers are already running
		return nil
	case "container_pause":
		// Unpause if it was paused
		return i.containerManager.UnpauseContainer(ctx, containerID)
	case "cpu_stress", "cpu", "memory_stress", "memory_pressure", "memory":
		// Remove stress faults
		return i.stressInjector.RemoveFault(ctx, containerID)
	case "connection_drop":
		return i.firewallInjector.RemoveFault(ctx, containerID)
	case "dns":
		return i.dnsInjector.RemoveFault(ctx, containerID)
	case "process_priority":
		// For process priority, we need the params to know which process
		// For now, just return nil - priority will reset on process restart
		return nil
	case "disk_io":
		// For disk I/O, we need the params to know the target path
		// For now, just return nil - ionice will reset on process restart
		return nil
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
