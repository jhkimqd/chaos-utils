package l3l4

import (
	"context"
	"fmt"

	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

// FaultParams defines parameters for L3/L4 network fault injection
type FaultParams struct {
	// Device is the network interface (default: eth0)
	Device string

	// Latency in milliseconds
	Latency int

	// PacketLoss as percentage (0-100)
	PacketLoss float64

	// Bandwidth limit in kbit/s
	Bandwidth int

	// Reorder percentage (0-100) - requires latency to be set
	Reorder int

	// ReorderCorrelation percentage (0-100) - correlation between reordered packets
	ReorderCorrelation int

	// TargetPorts is a comma-separated list of target ports (e.g., "80,443")
	TargetPorts string

	// TargetProto is the protocol to target (tcp, udp, or tcp,udp)
	TargetProto string

	// TargetIPs is a comma-separated list of target IP addresses
	TargetIPs string

	// TargetCIDR is a CIDR notation for target network
	TargetCIDR string
}

// ComcastWrapper is kept for backward compatibility but delegates to TCWrapper.
// All network faults now use tc directly instead of comcast, which relied on
// iptables mangle marks that don't work in Docker shared network namespaces.
type ComcastWrapper struct {
	tc *TCWrapper
}

// New creates a new ComcastWrapper (delegates to TCWrapper)
func New(sidecarMgr *sidecar.Manager) *ComcastWrapper {
	return &ComcastWrapper{
		tc: NewTCWrapper(sidecarMgr),
	}
}

// InjectFault injects a network fault on a target container using tc
func (cw *ComcastWrapper) InjectFault(ctx context.Context, targetContainerID string, params FaultParams) error {
	return cw.tc.InjectFault(ctx, targetContainerID, params)
}

// RemoveFault removes all faults from a target container
func (cw *ComcastWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	return cw.tc.RemoveFault(ctx, targetContainerID)
}

// ValidateFaultParams validates fault parameters
func ValidateFaultParams(params FaultParams) error {
	if params.Latency < 0 {
		return fmt.Errorf("latency cannot be negative")
	}

	if params.PacketLoss < 0 || params.PacketLoss > 100 {
		return fmt.Errorf("packet loss must be between 0 and 100")
	}

	if params.Bandwidth < 0 {
		return fmt.Errorf("bandwidth cannot be negative")
	}

	// Check that at least one fault is specified
	if params.Latency == 0 && params.PacketLoss == 0 && params.Bandwidth == 0 && params.Reorder == 0 {
		return fmt.Errorf("at least one fault type must be specified (latency, packet-loss, bandwidth, or reorder)")
	}

	// Reorder requires latency
	if params.Reorder > 0 && params.Latency == 0 {
		return fmt.Errorf("packet reordering requires latency to be set")
	}

	if params.Reorder < 0 || params.Reorder > 100 {
		return fmt.Errorf("reorder must be between 0 and 100")
	}

	if params.ReorderCorrelation < 0 || params.ReorderCorrelation > 100 {
		return fmt.Errorf("reorder_correlation must be between 0 and 100")
	}

	return nil
}
