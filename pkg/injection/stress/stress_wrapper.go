package stress

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

// StressParams defines parameters for CPU/memory stress injection
type StressParams struct {
	// Method is the stress method ("stress" for active load, "limit" for resource constraints)
	Method string

	// CPUPercent is the CPU load percentage (0-100)
	CPUPercent int

	// MemoryMB is the amount of memory to allocate in megabytes
	MemoryMB int

	// Duration is the stress duration (e.g., "4m", "30s")
	Duration string

	// Cores is the number of CPU cores to stress (0 = default 4)
	Cores int
}

// StressWrapper wraps resource constraint injection via Docker API
type StressWrapper struct {
	sidecarMgr   *sidecar.Manager
	dockerClient DockerClient
	// Store original container resources for restoration
	originalResources map[string]container.Resources
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error)
}

// New creates a new stress wrapper
func New(sidecarMgr *sidecar.Manager, dockerClient DockerClient) *StressWrapper {
	return &StressWrapper{
		sidecarMgr:        sidecarMgr,
		dockerClient:      dockerClient,
		originalResources: make(map[string]container.Resources),
	}
}

// InjectCPUStress injects CPU stress on a target container
func (sw *StressWrapper) InjectCPUStress(ctx context.Context, targetContainerID string, params StressParams) error {
	// Choose method based on params
	if params.Method == "stress" {
		return sw.injectActiveCPUStress(ctx, targetContainerID, params)
	}
	// Default to "limit" method
	return sw.injectCPULimit(ctx, targetContainerID, params)
}

// injectActiveCPUStress actively stresses CPU by running busy loops
func (sw *StressWrapper) injectActiveCPUStress(ctx context.Context, targetContainerID string, params StressParams) error {
	cores := params.Cores
	if cores == 0 {
		cores = 2 // default 2 cores for stress
	}

	cpuPercent := params.CPUPercent
	if cpuPercent == 0 {
		cpuPercent = 80 // default 80% load
	}

	fmt.Printf("Injecting active CPU stress on target %s: %d%% load on %d core(s)\n",
		targetContainerID[:12], cpuPercent, cores)

	// Build CPU stress command using shell built-ins
	// Use 'yes > /dev/null' for simple continuous CPU burn on each core
	var stressCmd string

	if cpuPercent >= 70 {
		// High load: Run continuous yes command for each core
		stressCmd = fmt.Sprintf("for i in $(seq 1 %d); do yes > /dev/null & done", cores)
	} else {
		// Moderate load: Run yes with periodic pauses
		// Use timeout to run yes for X seconds, then sleep
		burnSec := cpuPercent / 20  // seconds to burn (rough approximation)
		if burnSec < 1 {
			burnSec = 1
		}
		sleepSec := (100 - cpuPercent) / 20
		if sleepSec < 1 {
			sleepSec = 1
		}

		stressCmd = fmt.Sprintf(
			"for i in $(seq 1 %d); do while true; do timeout %d yes > /dev/null 2>/dev/null; sleep %d; done & done",
			cores, burnSec, sleepSec,
		)
	}

	// Execute stress command in background
	cmd := []string{"sh", "-c", stressCmd}

	_, err := sw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to inject active CPU stress: %w", err)
	}

	fmt.Printf("Active CPU stress injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// injectCPULimit limits CPU using cgroup constraints
func (sw *StressWrapper) injectCPULimit(ctx context.Context, targetContainerID string, params StressParams) error {
	// Get current container config to save original resources
	inspect, err := sw.dockerClient.ContainerInspect(ctx, targetContainerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	// Save original resources if not already saved
	if _, exists := sw.originalResources[targetContainerID]; !exists {
		sw.originalResources[targetContainerID] = container.Resources{
			NanoCPUs:   inspect.HostConfig.NanoCPUs,
			CPUQuota:   inspect.HostConfig.CPUQuota,
			CPUPeriod:  inspect.HostConfig.CPUPeriod,
			Memory:     inspect.HostConfig.Memory,
			MemorySwap: inspect.HostConfig.MemorySwap,
		}
	}

	cpuPercent := params.CPUPercent
	if cpuPercent == 0 {
		cpuPercent = 50 // default 50%
	}

	cores := params.Cores
	if cores == 0 {
		cores = 1 // default 1 core worth of CPU
	}

	// CPUPeriod is typically 100000 microseconds (100ms)
	cpuPeriod := int64(100000)
	// Calculate quota: (percent / 100) * cores * period
	cpuQuota := int64(float64(cpuPercent) / 100.0 * float64(cores) * float64(cpuPeriod))

	fmt.Printf("Injecting CPU limit on target %s: %d%% of %d core(s) (quota=%d, period=%d)\n",
		targetContainerID[:12], cpuPercent, cores, cpuQuota, cpuPeriod)

	// Update container with CPU limits
	updateConfig := container.UpdateConfig{
		Resources: container.Resources{
			CPUQuota:  cpuQuota,
			CPUPeriod: cpuPeriod,
		},
	}

	_, err = sw.dockerClient.ContainerUpdate(ctx, targetContainerID, updateConfig)
	if err != nil {
		return fmt.Errorf("failed to update container CPU limits: %w", err)
	}

	fmt.Printf("CPU limit injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// InjectMemoryStress injects memory pressure on a target container
// Note: Memory stress uses cgroup limits (method is ignored)
// Active memory allocation is unreliable without installing tools in target
// Use conservative limits (e.g., 512MB-2GB) to create pressure without OOM kills
func (sw *StressWrapper) InjectMemoryStress(ctx context.Context, targetContainerID string, params StressParams) error {
	// Always use limit method for memory
	// Active memory stress requires tools we can't rely on in target containers
	return sw.injectMemoryLimit(ctx, targetContainerID, params)
}

// injectMemoryLimit limits memory using cgroup constraints
func (sw *StressWrapper) injectMemoryLimit(ctx context.Context, targetContainerID string, params StressParams) error {
	// Get current container config to save original resources
	inspect, err := sw.dockerClient.ContainerInspect(ctx, targetContainerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	// Save original resources if not already saved
	if _, exists := sw.originalResources[targetContainerID]; !exists {
		sw.originalResources[targetContainerID] = container.Resources{
			NanoCPUs:   inspect.HostConfig.NanoCPUs,
			CPUQuota:   inspect.HostConfig.CPUQuota,
			CPUPeriod:  inspect.HostConfig.CPUPeriod,
			Memory:     inspect.HostConfig.Memory,
			MemorySwap: inspect.HostConfig.MemorySwap,
		}
	}

	// Calculate memory limit
	memoryMB := params.MemoryMB
	if memoryMB == 0 {
		memoryMB = 512 // default 512MB
	}

	memoryBytes := int64(memoryMB) * 1024 * 1024

	fmt.Printf("Injecting memory limit on target %s: %dMB\n", targetContainerID[:12], memoryMB)

	// Update container with memory limits
	// MemorySwap must be set >= Memory (setting to same value = no swap allowed)
	updateConfig := container.UpdateConfig{
		Resources: container.Resources{
			Memory:     memoryBytes,
			MemorySwap: memoryBytes, // Same as Memory = no swap
		},
	}

	_, err = sw.dockerClient.ContainerUpdate(ctx, targetContainerID, updateConfig)
	if err != nil {
		return fmt.Errorf("failed to update container memory limits: %w", err)
	}

	fmt.Printf("Memory limit injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes stress or restores original resource limits
func (sw *StressWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	fmt.Printf("Removing stress/limits from target %s\n", targetContainerID[:12])

	// Kill any active stress processes (yes, dd, etc.)
	// This handles the "stress" method
	killCmds := []string{
		"pkill -9 yes",
		"pkill -9 dd",
		"pkill -9 head",  // Kill memory allocation processes
		"pkill -9 tr",
		"pkill -9 sleep", // Kill sleep processes holding memory
		"rm -f /dev/shm/mem-stress-fill",
		"rm -f /tmp/mem-stress-fill",
	}

	for _, cmd := range killCmds {
		_, _ = sw.dockerClient.ExecCommand(ctx, targetContainerID, []string{"sh", "-c", cmd})
		// Ignore errors - processes may not exist
	}

	// Restore original resource limits (for "limit" method)
	originalRes, exists := sw.originalResources[targetContainerID]
	if !exists {
		// No original resources saved, only stress cleanup was needed
		fmt.Printf("Stress processes killed on target %s\n", targetContainerID[:12])
		return nil
	}

	// Restore original resource limits
	restoreConfig := container.Resources{}

	// Restore Memory limits
	if originalRes.Memory == 0 {
		// Original had no memory limit (0)
		// Docker doesn't allow setting Memory=0 or Memory=-1 via update
		// Workaround: Set to a very high value (1TB) to effectively remove the limit
		restoreConfig.Memory = 1024 * 1024 * 1024 * 1024  // 1TB
		restoreConfig.MemorySwap = restoreConfig.Memory    // MemorySwap must be >= Memory
	} else {
		// Container had specific memory limits - restore them exactly
		restoreConfig.Memory = originalRes.Memory
		if originalRes.MemorySwap == 0 {
			// If MemorySwap was 0, set to same as Memory (no swap)
			restoreConfig.MemorySwap = originalRes.Memory
		} else {
			restoreConfig.MemorySwap = originalRes.MemorySwap
		}
	}

	// If original CPU quota was 0 (no limit), set to -1 to explicitly remove the limit
	if originalRes.CPUQuota == 0 {
		restoreConfig.CPUQuota = -1
	} else {
		restoreConfig.CPUQuota = originalRes.CPUQuota
		restoreConfig.CPUPeriod = originalRes.CPUPeriod
	}

	updateConfig := container.UpdateConfig{
		Resources: restoreConfig,
	}

	_, err := sw.dockerClient.ContainerUpdate(ctx, targetContainerID, updateConfig)
	if err != nil {
		return fmt.Errorf("failed to restore container resource limits: %w", err)
	}

	// Remove from tracking
	delete(sw.originalResources, targetContainerID)

	fmt.Printf("Stress removed and limits restored on target %s\n", targetContainerID[:12])

	return nil
}



// ValidateStressParams validates stress parameters
func ValidateStressParams(params StressParams) error {
	if params.CPUPercent < 0 || params.CPUPercent > 100 {
		return fmt.Errorf("cpu_percent must be between 0 and 100")
	}

	if params.MemoryMB < 0 {
		return fmt.Errorf("memory_mb cannot be negative")
	}

	if params.Cores < 0 {
		return fmt.Errorf("cores cannot be negative")
	}

	return nil
}
