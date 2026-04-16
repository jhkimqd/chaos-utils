package injection

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/container"
	"github.com/jihwankim/chaos-utils/pkg/injection/disk"
	"github.com/jihwankim/chaos-utils/pkg/injection/dns"
	"github.com/jihwankim/chaos-utils/pkg/injection/firewall"
	"github.com/jihwankim/chaos-utils/pkg/injection/l3l4"
	chaosp2p "github.com/jihwankim/chaos-utils/pkg/injection/p2p/bor"
	"github.com/jihwankim/chaos-utils/pkg/injection/process"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
	"github.com/jihwankim/chaos-utils/pkg/injection/stress"
	chaoshttp "github.com/jihwankim/chaos-utils/pkg/injection/http"
	chaostime "github.com/jihwankim/chaos-utils/pkg/injection/time"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
	"github.com/rs/zerolog/log"
)

// Target represents a fault injection target
type Target struct {
	Name        string
	ContainerID string
}

// Injector provides unified interface for all fault types
type Injector struct {
	tcInjector       *l3l4.TCWrapper
	containerManager *container.Manager
	stressInjector   *stress.StressWrapper
	firewallInjector *firewall.IptablesWrapper
	dnsInjector      *dns.DNSWrapper
	processInjector  *process.PriorityWrapper
	diskInjector     *disk.IODelayWrapper
	diskFillInjector *disk.FillWrapper
	fileOpsInjector  *disk.FileOpsWrapper
	clockInjector    *chaostime.ClockSkewWrapper
	httpInjector     *chaoshttp.HTTPFaultWrapper
	sidecarMgr       *sidecar.Manager
	dockerClient     *docker.Client
}

// New creates a new unified fault injector
func New(sidecarMgr *sidecar.Manager, dockerClient *docker.Client) *Injector {
	return &Injector{
		tcInjector:       l3l4.NewTCWrapper(sidecarMgr),
		containerManager: container.NewManager(dockerClient.GetClient()),
		stressInjector:   stress.New(dockerClient),
		firewallInjector: firewall.New(sidecarMgr),
		dnsInjector:      dns.New(sidecarMgr),
		processInjector:  process.New(dockerClient),
		diskInjector:     disk.New(dockerClient),
		diskFillInjector: disk.NewFillWrapper(dockerClient),
		fileOpsInjector:  disk.NewFileOpsWrapper(dockerClient),
		clockInjector:    chaostime.New(dockerClient),
		httpInjector:     chaoshttp.New(sidecarMgr),
		sidecarMgr:       sidecarMgr,
		dockerClient:     dockerClient,
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
	case "disk_fill":
		return i.injectDiskFill(ctx, fault, targets)
	case "file_delete":
		return i.injectFileDelete(ctx, fault, targets)
	case "file_corrupt":
		return i.injectFileCorrupt(ctx, fault, targets)
	case "clock_skew":
		return i.injectClockSkew(ctx, fault, targets)
	case "process_kill":
		return i.injectProcessKill(ctx, fault, targets)
	case "http_fault":
		return i.injectHTTPFault(ctx, fault, targets)
	case "corruption_proxy":
		return i.injectCorruptionProxy(ctx, fault, targets)
	case "p2p_attack":
		return i.injectP2PAttack(ctx, fault, targets)
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
		if reorder, ok := fault.Params["reorder"].(int); ok {
			params.Reorder = reorder
		}
		if reorderCorr, ok := fault.Params["reorder_correlation"].(int); ok {
			params.ReorderCorrelation = reorderCorr
		}
		if corrupt, ok := fault.Params["corrupt"].(float64); ok {
			params.Corrupt = corrupt
		} else if corrupt, ok := fault.Params["corrupt"].(int); ok {
			params.Corrupt = float64(corrupt)
		}
		if duplicate, ok := fault.Params["duplicate"].(float64); ok {
			params.Duplicate = duplicate
		} else if duplicate, ok := fault.Params["duplicate"].(int); ok {
			params.Duplicate = float64(duplicate)
		}
	}

	// Use tc directly for all network faults (latency, loss, reorder, port filtering)
	for _, target := range targets {
		if err := i.tcInjector.InjectFault(ctx, target.ContainerID, params); err != nil {
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
		Method:      "dd",
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
		return i.tcInjector.RemoveFault(ctx, containerID)
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
		// Removes dm-delay mapping if active, otherwise kills dd stress processes
		return i.diskInjector.RemoveFault(ctx, containerID, disk.IODelayParams{Operation: "all"})
	case "disk_fill":
		return i.diskFillInjector.RemoveFault(ctx, containerID)
	case "file_delete", "file_corrupt":
		// Restore any .chaos_backup files left by backup_first=true
		return i.fileOpsInjector.RestoreAllBackups(ctx, containerID)
	case "clock_skew":
		return i.clockInjector.RemoveClockSkew(ctx, containerID, chaostime.ClockSkewParams{})
	case "process_kill":
		// Process kill is a one-shot action, nothing to remove
		return nil
	case "http_fault":
		return i.httpInjector.RemoveAllFaults(ctx, containerID)
	case "corruption_proxy":
		return i.removeCorruptionProxy(ctx, containerID)
	case "p2p_attack":
		// P2P attacks are short-lived connections; the peer disconnects when done.
		// Nothing to clean up on the target side.
		return nil
	default:
		return fmt.Errorf("unknown fault type for removal: %s", faultType)
	}
}

// injectDiskFill handles disk fill injection
func (i *Injector) injectDiskFill(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := disk.FillParams{}

	if fault.Params != nil {
		if targetPath, ok := fault.Params["target_path"].(string); ok {
			params.TargetPath = targetPath
		}
		if sizeMB, ok := fault.Params["size_mb"].(int); ok {
			params.SizeMB = sizeMB
		} else if sizeMB, ok := fault.Params["size_mb"].(float64); ok {
			params.SizeMB = int(sizeMB)
		}
		if fillPercent, ok := fault.Params["fill_percent"].(int); ok {
			params.FillPercent = fillPercent
		} else if fillPercent, ok := fault.Params["fill_percent"].(float64); ok {
			params.FillPercent = int(fillPercent)
		}
		if fileName, ok := fault.Params["file_name"].(string); ok {
			params.FileName = fileName
		}
	}

	if err := disk.ValidateFillParams(params); err != nil {
		return fmt.Errorf("invalid disk fill parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.diskFillInjector.InjectDiskFill(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject disk fill on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectFileDelete handles file deletion injection
func (i *Injector) injectFileDelete(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := disk.FileDeleteParams{}

	if fault.Params != nil {
		if targetPath, ok := fault.Params["target_path"].(string); ok {
			params.TargetPath = targetPath
		}
		if recursive, ok := fault.Params["recursive"].(bool); ok {
			params.Recursive = recursive
		}
		if backupFirst, ok := fault.Params["backup_first"].(bool); ok {
			params.BackupFirst = backupFirst
		}
	}

	if err := disk.ValidateFileDeleteParams(params); err != nil {
		return fmt.Errorf("invalid file delete parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.fileOpsInjector.InjectFileDelete(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject file delete on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectFileCorrupt handles file corruption injection
func (i *Injector) injectFileCorrupt(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := disk.FileCorruptParams{
		CorruptBytes: 512,
		Method:       "random",
	}

	if fault.Params != nil {
		if targetPath, ok := fault.Params["target_path"].(string); ok {
			params.TargetPath = targetPath
		}
		if corruptBytes, ok := fault.Params["corrupt_bytes"].(int); ok {
			params.CorruptBytes = corruptBytes
		}
		if corruptOffset, ok := fault.Params["corrupt_offset"].(int); ok {
			params.CorruptOffset = corruptOffset
		}
		if method, ok := fault.Params["method"].(string); ok {
			params.Method = method
		}
		if backupFirst, ok := fault.Params["backup_first"].(bool); ok {
			params.BackupFirst = backupFirst
		}
	}

	if err := disk.ValidateFileCorruptParams(params); err != nil {
		return fmt.Errorf("invalid file corrupt parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.fileOpsInjector.InjectFileCorrupt(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject file corruption on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectClockSkew handles clock skew injection
func (i *Injector) injectClockSkew(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := chaostime.ClockSkewParams{}

	if fault.Params != nil {
		if offset, ok := fault.Params["offset"].(string); ok {
			params.Offset = offset
		}
		if disableNTP, ok := fault.Params["disable_ntp"].(bool); ok {
			params.DisableNTP = disableNTP
		}
	}

	if err := chaostime.ValidateClockSkewParams(params); err != nil {
		return fmt.Errorf("invalid clock skew parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.clockInjector.InjectClockSkew(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject clock skew on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectProcessKill handles process kill injection
func (i *Injector) injectProcessKill(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := process.KillParams{
		Signal: "KILL",
		Count:  1,
	}

	if fault.Params != nil {
		if processPattern, ok := fault.Params["process_pattern"].(string); ok {
			params.ProcessPattern = processPattern
		}
		if signal, ok := fault.Params["signal"].(string); ok {
			params.Signal = signal
		}
		if killChildren, ok := fault.Params["kill_children"].(bool); ok {
			params.KillChildren = killChildren
		}
		if interval, ok := fault.Params["interval"].(int); ok {
			params.Interval = interval
		}
		if count, ok := fault.Params["count"].(int); ok {
			params.Count = count
		}
	}

	if err := process.ValidateKillParams(params); err != nil {
		return fmt.Errorf("invalid process kill parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.processInjector.InjectProcessKill(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject process kill on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectHTTPFault handles HTTP fault injection via Envoy proxy
func (i *Injector) injectHTTPFault(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	params := chaoshttp.HTTPFaultParams{
		DelayPercent: 100,
		AbortPercent: 100,
	}

	if fault.Params != nil {
		if targetPort, ok := fault.Params["target_port"].(int); ok {
			params.TargetPort = targetPort
		} else if targetPort, ok := fault.Params["target_port"].(float64); ok {
			params.TargetPort = int(targetPort)
		}
		if delayMs, ok := fault.Params["delay_ms"].(int); ok {
			params.DelayMs = delayMs
		} else if delayMs, ok := fault.Params["delay_ms"].(float64); ok {
			params.DelayMs = int(delayMs)
		}
		if delayPercent, ok := fault.Params["delay_percent"].(int); ok {
			params.DelayPercent = delayPercent
		}
		if abortCode, ok := fault.Params["abort_code"].(int); ok {
			params.AbortCode = abortCode
		} else if abortCode, ok := fault.Params["abort_code"].(float64); ok {
			params.AbortCode = int(abortCode)
		}
		if abortPercent, ok := fault.Params["abort_percent"].(int); ok {
			params.AbortPercent = abortPercent
		}
		if bodyOverride, ok := fault.Params["body_override"].(string); ok {
			params.BodyOverride = bodyOverride
		}
		if pathPattern, ok := fault.Params["path_pattern"].(string); ok {
			params.PathPattern = pathPattern
		}
		if headerOverrides, ok := fault.Params["header_overrides"].(map[string]interface{}); ok {
			params.HeaderOverrides = make(map[string]string)
			for k, v := range headerOverrides {
				if s, ok := v.(string); ok {
					params.HeaderOverrides[k] = s
				}
			}
		}
	}

	if err := chaoshttp.ValidateHTTPFaultParams(params); err != nil {
		return fmt.Errorf("invalid HTTP fault parameters: %w", err)
	}

	for _, target := range targets {
		if err := i.httpInjector.InjectHTTPFault(ctx, target.ContainerID, params); err != nil {
			return fmt.Errorf("failed to inject HTTP fault on %s: %w", target.Name, err)
		}
	}

	return nil
}

// injectP2PAttack handles P2P fault injection by connecting directly to a Bor
// node's devp2p port and sending crafted malicious Ethereum protocol messages.
//
// Required parameters:
//   - rpc_url  string  — Bor JSON-RPC URL for admin_nodeInfo enode discovery
//                        (alternative: enode_url with a pre-resolved enode URL)
//   - attack   string  — attack type (malformed-block | conflicting-chain |
//                        invalid-txs | malicious-status | invalid-range |
//                        flood-hashes | header-flood)
//
// Optional parameters:
//   - count      int    — number of messages to send (default 1)
//   - fork_block uint64 — fork point for conflicting-chain (default 100)
//   - enode_url  string — direct enode URL, bypasses admin_nodeInfo lookup
func (i *Injector) injectP2PAttack(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	// Parse parameters.
	attackType := "malformed-block"
	count := 1
	forkBlock := uint64(100)
	var rpcURL, enodeURL string

	if fault.Params != nil {
		if v, ok := fault.Params["attack"].(string); ok {
			attackType = v
		}
		if v, ok := fault.Params["count"].(int); ok {
			count = v
		} else if v, ok := fault.Params["count"].(float64); ok {
			count = int(v)
		}
		if v, ok := fault.Params["fork_block"].(int); ok {
			forkBlock = uint64(v)
		} else if v, ok := fault.Params["fork_block"].(float64); ok {
			forkBlock = uint64(v)
		}
		if v, ok := fault.Params["rpc_url"].(string); ok {
			rpcURL = v
		}
		if v, ok := fault.Params["enode_url"].(string); ok {
			enodeURL = v
		}
	}

	const maxP2PAttackCount = 100000
	if count > maxP2PAttackCount {
		return fmt.Errorf("p2p_attack count %d exceeds maximum %d", count, maxP2PAttackCount)
	}

	logger := log.With().Str("fault", "p2p_attack").Str("attack", attackType).Logger()

	for _, target := range targets {
		resolvedEnode := enodeURL

		// If no direct enode_url, discover via admin_nodeInfo RPC.
		if resolvedEnode == "" {
			targetRPC := rpcURL
			// Auto-derive RPC URL from the container's Docker network IP
			// when rpc_url is not provided or contains unresolved variables.
			if targetRPC == "" || strings.Contains(targetRPC, "${") {
				containerIP := i.getContainerIP(ctx, target.ContainerID)
				if containerIP == "" {
					return fmt.Errorf("p2p_attack on target %s: could not determine container IP (provide rpc_url or enode_url)", target.Name)
				}
				targetRPC = fmt.Sprintf("http://%s:8545", containerIP)
				logger.Info().Str("target", target.Name).Str("rpc", targetRPC).Msg("auto-discovered RPC URL from container IP")
			}
			discovered, err := chaosp2p.DiscoverSingleEnode(ctx, targetRPC)
			if err != nil {
				return fmt.Errorf("discover enode for target %s via %s: %w", target.Name, targetRPC, err)
			}
			resolvedEnode = discovered
		}

		logger.Info().
			Str("target", target.Name).
			Str("enode", resolvedEnode).
			Msg("launching P2P attack")

		peer, err := chaosp2p.NewChaosPeer(logger)
		if err != nil {
			return fmt.Errorf("create chaos peer for %s: %w", target.Name, err)
		}

		if err := peer.Connect(resolvedEnode); err != nil {
			return fmt.Errorf("connect to %s (%s): %w", target.Name, resolvedEnode, err)
		}

		attackErr := runP2PAttack(ctx, peer, attackType, count, forkBlock)
		peer.Close()

		if attackErr != nil {
			// A disconnect from the target is not a framework error — it means
			// the attack was successful enough that Bor ejected the peer.
			logger.Warn().
				Err(attackErr).
				Str("target", target.Name).
				Msg("P2P attack ended (peer may have been disconnected by target)")
		}
	}

	return nil
}

// runP2PAttack executes a single named attack on the given peer.
func runP2PAttack(ctx context.Context, peer *chaosp2p.ChaosPeer, attackType string, count int, forkBlock uint64) error {
	switch attackType {
	case "malformed-block":
		for j := 0; j < count; j++ {
			if err := peer.SendMalformedBlock(uint64(j + 1)); err != nil {
				return err
			}
		}
		return nil

	case "conflicting-chain":
		for j := 0; j < count; j++ {
			if err := peer.SendConflictingChain(forkBlock); err != nil {
				return err
			}
		}
		return nil

	case "invalid-txs":
		return peer.SendInvalidTransactions(count)

	case "malicious-status":
		return peer.SendMaliciousStatus()

	case "invalid-range":
		return peer.SendInvalidBlockRange()

	case "flood-hashes":
		return peer.FloodNewBlockHashes(ctx, count)

	case "header-flood":
		return peer.SendGetBlockHeadersFlood(ctx, count)

	default:
		return fmt.Errorf("unknown P2P attack type: %q", attackType)
	}
}

// getContainerIP returns the first Docker network IP of a container.
func (i *Injector) getContainerIP(ctx context.Context, containerID string) string {
	info, err := i.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return ""
	}
	for _, network := range info.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress
		}
	}
	return ""
}

// injectCorruptionProxy deploys the Go corruption proxy (Phase 2) in the
// sidecar for the given targets. It:
//  1. Writes a YAML rules file to the sidecar at /tmp/corruption-rules-<port>.yaml
//  2. Starts the corruption-proxy binary in the background
//  3. Installs the same iptables PREROUTING REDIRECT rule as the Envoy injector
//
// Required fault params:
//
//	target_port  int    – the port to intercept (e.g. 1317, 8545, 26657)
//	rules_yaml   string – inline YAML rules content to write to the sidecar
//
// The corruption-proxy binary must already be present at
// /usr/local/bin/corruption-proxy in the sidecar image. Build and include it:
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
//	  -o bin/corruption-proxy ./cmd/corruption-proxy
//	# Then in Dockerfile.sidecar:
//	# COPY bin/corruption-proxy /usr/local/bin/corruption-proxy
func (i *Injector) injectCorruptionProxy(ctx context.Context, fault *scenario.Fault, targets []Target) error {
	targetPort := 1317 // default: Heimdall REST API
	rulesYAML := ""

	if fault.Params != nil {
		if tp, ok := fault.Params["target_port"].(int); ok {
			targetPort = tp
		} else if tp, ok := fault.Params["target_port"].(float64); ok {
			targetPort = int(tp)
		}
		if ry, ok := fault.Params["rules_yaml"].(string); ok {
			rulesYAML = ry
		}
	}

	if targetPort <= 0 || targetPort > 65535 {
		return fmt.Errorf("corruption_proxy: target_port must be between 1 and 65535")
	}
	if rulesYAML == "" {
		return fmt.Errorf("corruption_proxy: rules_yaml is required")
	}

	// Proxy port follows the same 15000+targetPort convention as Envoy.
	proxyPort := 15000 + targetPort
	controlPort := proxyPort + 1
	rulesPath := fmt.Sprintf("/tmp/corruption-rules-%d.yaml", targetPort)
	logPath := fmt.Sprintf("/tmp/corruption-proxy-%d.log", targetPort)

	for _, target := range targets {
		// Ensure sidecar exists.
		if _, exists := i.sidecarMgr.GetSidecarID(target.ContainerID); !exists {
			fmt.Printf("Creating sidecar for corruption proxy on target %s\n", target.ContainerID[:12])
			if _, err := i.sidecarMgr.CreateSidecar(ctx, target.ContainerID); err != nil {
				return fmt.Errorf("failed to create sidecar for %s: %w", target.Name, err)
			}
		}

		// Step 1: Write rules YAML to sidecar.
		encodedRules := base64.StdEncoding.EncodeToString([]byte(rulesYAML))
		writeCmd := []string{"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", encodedRules, rulesPath)}
		if out, err := i.sidecarMgr.ExecInSidecar(ctx, target.ContainerID, writeCmd); err != nil {
			return fmt.Errorf("failed to write rules to sidecar %s: %w (output: %s)", target.Name, err, out)
		}

		// Step 2: Start corruption-proxy in background.
		startCmd := []string{"sh", "-c", fmt.Sprintf(
			"corruption-proxy --listen :%d --target %d --rules %s --control 127.0.0.1:%d > %s 2>&1 &",
			proxyPort, targetPort, rulesPath, controlPort, logPath,
		)}
		fmt.Printf("Starting corruption proxy on port %d for target %s (port %d)\n",
			proxyPort, target.ContainerID[:12], targetPort)
		if out, err := i.sidecarMgr.ExecInSidecar(ctx, target.ContainerID, startCmd); err != nil {
			return fmt.Errorf("failed to start corruption proxy on %s: %w (output: %s)", target.Name, err, out)
		}

		// Step 3: Wait for proxy to be ready (poll control API).
		readyCmd := []string{"sh", "-c", fmt.Sprintf(
			"for i in $(seq 1 30); do "+
				"if curl -sf http://127.0.0.1:%d/stats > /dev/null 2>&1; then exit 0; fi; "+
				"sleep 0.5; done; exit 1",
			controlPort,
		)}
		if out, err := i.sidecarMgr.ExecInSidecar(ctx, target.ContainerID, readyCmd); err != nil {
			logCmd := []string{"sh", "-c", fmt.Sprintf("tail -20 %s 2>/dev/null || echo 'no logs'", logPath)}
			logs, _ := i.sidecarMgr.ExecInSidecar(ctx, target.ContainerID, logCmd)
			return fmt.Errorf("corruption proxy failed to start on %s within 15s: %w (output: %s, logs: %s)",
				target.Name, err, out, logs)
		}

		// Step 4: Install iptables PREROUTING redirect.
		iptCmd := []string{
			"iptables", "-t", "nat", "-A", "PREROUTING",
			"-p", "tcp", "--dport", fmt.Sprintf("%d", targetPort),
			"-j", "REDIRECT", "--to-port", fmt.Sprintf("%d", proxyPort),
			"-m", "comment", "--comment", "chaos-corruption-proxy",
		}
		fmt.Printf("  iptables: %s\n", strings.Join(iptCmd, " "))
		if out, err := i.sidecarMgr.ExecInSidecar(ctx, target.ContainerID, iptCmd); err != nil {
			fmt.Printf("  Warning: iptables command failed: %v (output: %s)\n", err, out)
		}

		fmt.Printf("Corruption proxy active on target %s (port %d → proxy:%d, control:%d)\n",
			target.ContainerID[:12], targetPort, proxyPort, controlPort)
	}

	return nil
}

// removeCorruptionProxy stops all running corruption-proxy instances in the
// sidecar and removes the iptables PREROUTING redirect rules they installed.
func (i *Injector) removeCorruptionProxy(ctx context.Context, containerID string) error {
	if _, exists := i.sidecarMgr.GetSidecarID(containerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", containerID)
	}

	fmt.Printf("Removing corruption proxy from target %s\n", containerID[:12])

	// Remove all chaos-corruption-proxy iptables rules.
	// Use iptables-save to find exact rule specs, then delete them.
	// The partial-match approach (just comment + REDIRECT) does not work because
	// iptables -D requires an exact match of all fields.
	cleanupIPT := []string{"sh", "-c",
		"iptables-save -t nat 2>/dev/null | grep 'chaos-corruption-proxy' | sed 's/^-A /-D /' | " +
			"while IFS= read -r rule; do iptables -t nat $rule 2>/dev/null; done; echo done"}
	_, iptErr := i.sidecarMgr.ExecInSidecar(ctx, containerID, cleanupIPT)
	if iptErr != nil {
		log.Warn().Err(iptErr).Str("container", containerID[:12]).Msg("failed to remove corruption-proxy iptables rules")
	}

	// Kill all corruption-proxy processes and remove temp files.
	killCmd := []string{"sh", "-c",
		"for p in /proc/[0-9]*/cmdline; do " +
			"PID=$(echo $p | cut -d/ -f3); " +
			"if tr '\\0' ' ' < $p 2>/dev/null | grep -q corruption-proxy; then kill -9 $PID 2>/dev/null; fi; " +
			"done; " +
			"rm -f /tmp/corruption-rules-*.yaml /tmp/corruption-proxy-*.log 2>/dev/null; " +
			"echo done"}
	_, killErr := i.sidecarMgr.ExecInSidecar(ctx, containerID, killCmd)
	if killErr != nil {
		log.Warn().Err(killErr).Str("container", containerID[:12]).Msg("failed to kill corruption-proxy processes")
	}

	fmt.Printf("Corruption proxy removed from target %s\n", containerID[:12])
	return nil
}
