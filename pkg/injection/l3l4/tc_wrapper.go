package l3l4

import (
	"context"
	"fmt"
	"strings"
)

// TCWrapper handles direct tc commands for advanced network faults
type TCWrapper struct {
	sidecarMgr SidecarManager
}

// SidecarManager interface for sidecar operations
type SidecarManager interface {
	CreateSidecar(ctx context.Context, targetContainerID string) (string, error)
	ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error)
	GetSidecarID(targetContainerID string) (string, bool)
}

// NewTCWrapper creates a new TC wrapper
func NewTCWrapper(sidecarMgr SidecarManager) *TCWrapper {
	return &TCWrapper{
		sidecarMgr: sidecarMgr,
	}
}

// InjectPacketReorder injects packet reordering using tc netem
func (tw *TCWrapper) InjectPacketReorder(ctx context.Context, targetContainerID string, params FaultParams) error {
	// Ensure sidecar exists
	if _, exists := tw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := tw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	// Build tc netem command
	cmd := tw.buildTCNetemCommand(params)

	fmt.Printf("Injecting packet reorder on target %s: %s\n", targetContainerID[:12], strings.Join(cmd, " "))

	// Execute command in sidecar
	output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to inject packet reorder: %w (output: %s)", err, output)
	}

	fmt.Printf("Packet reorder injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes tc rules
func (tw *TCWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	if _, exists := tw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing tc rules from target %s\n", targetContainerID[:12])

	// Remove root qdisc (removes all tc rules)
	cmd := []string{"tc", "qdisc", "del", "dev", "eth0", "root"}

	_, _ = tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	// Ignore errors - rules might not exist

	fmt.Printf("TC rules removed successfully from target %s\n", targetContainerID[:12])

	return nil
}

// buildTCNetemCommand builds tc netem command for packet reordering
func (tw *TCWrapper) buildTCNetemCommand(params FaultParams) []string {
	device := params.Device
	if device == "" {
		device = "eth0"
	}

	// Base command: tc qdisc add dev eth0 root netem
	cmd := []string{"tc", "qdisc", "add", "dev", device, "root", "netem"}

	// Add delay (required for reorder)
	if params.Latency > 0 {
		cmd = append(cmd, "delay", fmt.Sprintf("%dms", params.Latency))
	}

	// Add packet reordering
	if params.Reorder > 0 {
		cmd = append(cmd, "reorder", fmt.Sprintf("%d%%", params.Reorder))
		if params.ReorderCorrelation > 0 {
			cmd = append(cmd, fmt.Sprintf("%d%%", params.ReorderCorrelation))
		}
	}

	return cmd
}
