package l3l4

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

// FaultParams defines parameters for L3/L4 network fault injection
type FaultParams struct {
	// Device is the network interface (default: eth0)
	Device string

	// Latency in milliseconds
	Latency int

	// Jitter in milliseconds (variation in latency)
	Jitter int

	// PacketLoss as percentage (0-100)
	PacketLoss float64

	// Bandwidth limit in kbit/s
	Bandwidth int

	// TargetPorts is a comma-separated list of target ports (e.g., "80,443")
	TargetPorts string

	// TargetProto is the protocol to target (tcp, udp, or tcp,udp)
	TargetProto string

	// TargetIPs is a comma-separated list of target IP addresses
	TargetIPs string

	// TargetCIDR is a CIDR notation for target network
	TargetCIDR string
}

// ComcastWrapper wraps the comcast CLI tool for fault injection via sidecars
type ComcastWrapper struct {
	sidecarMgr *sidecar.Manager
}

// New creates a new comcast wrapper
func New(sidecarMgr *sidecar.Manager) *ComcastWrapper {
	return &ComcastWrapper{
		sidecarMgr: sidecarMgr,
	}
}

// InjectFault injects a network fault on a target container
func (cw *ComcastWrapper) InjectFault(ctx context.Context, targetContainerID string, params FaultParams) error {
	// Ensure sidecar exists for target
	if _, exists := cw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := cw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	// Always run comcast --stop first to clear any existing rules
	// This prevents "rules already setup" errors from previous failed/interrupted tests
	stopCmd := []string{"comcast", "--stop"}
	_, _ = cw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, stopCmd) // Ignore errors - may not have rules

	// Build comcast command
	cmd := cw.buildComcastCommand(params)

	fmt.Printf("Injecting fault on target %s: %s\n", targetContainerID[:12], strings.Join(cmd, " "))

	// Execute command in sidecar
	output, err := cw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to inject fault: %w (output: %s)", err, output)
	}

	fmt.Printf("Fault injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes all faults from a target container
func (cw *ComcastWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	// Check if sidecar exists
	if _, exists := cw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing faults from target %s\n", targetContainerID[:12])

	// Execute comcast --stop
	cmd := []string{"comcast", "--stop"}

	output, err := cw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to remove fault: %w (output: %s)", err, output)
	}

	fmt.Printf("Faults removed successfully from target %s\n", targetContainerID[:12])

	return nil
}

// buildComcastCommand builds a comcast CLI command from fault parameters
func (cw *ComcastWrapper) buildComcastCommand(params FaultParams) []string {
	cmd := []string{"comcast"}

	// Device
	if params.Device != "" {
		cmd = append(cmd, "--device", params.Device)
	}

	// Latency
	if params.Latency > 0 {
		cmd = append(cmd, "--latency", fmt.Sprintf("%d", params.Latency))
	}

	// Jitter
	if params.Jitter > 0 {
		cmd = append(cmd, "--jitter", fmt.Sprintf("%d", params.Jitter))
	}

	// Packet loss
	if params.PacketLoss > 0 {
		cmd = append(cmd, "--packet-loss", fmt.Sprintf("%.2f", params.PacketLoss))
	}

	// Bandwidth
	if params.Bandwidth > 0 {
		cmd = append(cmd, "--bandwidth", fmt.Sprintf("%d", params.Bandwidth))
	}

	// Target ports
	if params.TargetPorts != "" {
		cmd = append(cmd, "--target-port", params.TargetPorts)
	}

	// Target protocol
	if params.TargetProto != "" {
		cmd = append(cmd, "--target-proto", params.TargetProto)
	}

	// Target IPs
	if params.TargetIPs != "" {
		cmd = append(cmd, "--target-addr", params.TargetIPs)
	}

	// Target CIDR
	if params.TargetCIDR != "" {
		cmd = append(cmd, "--target-addr", params.TargetCIDR)
	}

	return cmd
}

// ValidateFaultParams validates fault parameters
func ValidateFaultParams(params FaultParams) error {
	if params.Latency < 0 {
		return fmt.Errorf("latency cannot be negative")
	}

	if params.Jitter < 0 {
		return fmt.Errorf("jitter cannot be negative")
	}

	if params.PacketLoss < 0 || params.PacketLoss > 100 {
		return fmt.Errorf("packet loss must be between 0 and 100")
	}

	if params.Bandwidth < 0 {
		return fmt.Errorf("bandwidth cannot be negative")
	}

	// Check that at least one fault is specified
	if params.Latency == 0 && params.PacketLoss == 0 && params.Bandwidth == 0 {
		return fmt.Errorf("at least one fault type must be specified (latency, packet-loss, or bandwidth)")
	}

	return nil
}
