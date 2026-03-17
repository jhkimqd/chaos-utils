package l3l4

import (
	"fmt"
)

// FaultParams defines parameters for L3/L4 network fault injection via tc netem
type FaultParams struct {
	// Device is the network interface (default: eth0)
	Device string

	// Latency in milliseconds
	Latency int

	// PacketLoss as percentage (0-100)
	PacketLoss float64

	// Bandwidth limit in kbit/s (applied via tc netem rate)
	Bandwidth int

	// Reorder percentage (0-100) - requires latency to be set
	Reorder int

	// ReorderCorrelation percentage (0-100) - correlation between reordered packets
	ReorderCorrelation int

	// Corrupt percentage (0-100) - probability of packet corruption
	Corrupt float64

	// Duplicate percentage (0-100) - probability of packet duplication
	Duplicate float64

	// TargetPorts is a comma-separated list of target ports (e.g., "80,443")
	TargetPorts string

	// TargetProto is the protocol to target (tcp, udp, or tcp,udp)
	TargetProto string
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

	if params.Corrupt < 0 || params.Corrupt > 100 {
		return fmt.Errorf("corrupt must be between 0 and 100")
	}

	if params.Duplicate < 0 || params.Duplicate > 100 {
		return fmt.Errorf("duplicate must be between 0 and 100")
	}

	// Check that at least one fault is specified
	if params.Latency == 0 && params.PacketLoss == 0 && params.Bandwidth == 0 && params.Reorder == 0 && params.Corrupt == 0 && params.Duplicate == 0 {
		return fmt.Errorf("at least one fault type must be specified (latency, packet-loss, bandwidth, reorder, corrupt, or duplicate)")
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
