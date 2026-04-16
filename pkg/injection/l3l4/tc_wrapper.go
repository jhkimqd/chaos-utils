package l3l4

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// TCWrapper handles all network fault injection using tc directly.
// This replaces comcast which used iptables mangle marks for port filtering —
// a mechanism that doesn't work reliably in Docker shared network namespaces.
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

// InjectFault injects a network fault using tc commands.
// When port filtering is specified, uses a prio qdisc with u32 filters.
// Otherwise, uses a simple root netem qdisc.
func (tw *TCWrapper) InjectFault(ctx context.Context, targetContainerID string, params FaultParams) error {
	if err := tw.ensureSidecar(ctx, targetContainerID); err != nil {
		return err
	}

	tw.clearRules(ctx, targetContainerID, params.Device)

	if params.TargetPorts != "" {
		return tw.injectWithPortFilter(ctx, targetContainerID, params)
	}

	return tw.injectWholeDevice(ctx, targetContainerID, params)
}

// RemoveFault removes all tc rules from the device
func (tw *TCWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	if _, exists := tw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing tc rules from target %s\n", targetContainerID[:12])

	cmd := []string{"tc", "qdisc", "del", "dev", "eth0", "root"}
	_, tcErr := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if tcErr != nil {
		log.Warn().Err(tcErr).Str("container", targetContainerID[:12]).Msg("failed to remove tc qdisc during fault removal")
	}

	fmt.Printf("TC rules removed successfully from target %s\n", targetContainerID[:12])
	return nil
}

func (tw *TCWrapper) ensureSidecar(ctx context.Context, targetContainerID string) error {
	if _, exists := tw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := tw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}
	return nil
}

func (tw *TCWrapper) clearRules(ctx context.Context, targetContainerID string, device string) {
	if device == "" {
		device = "eth0"
	}
	cmd := []string{"tc", "qdisc", "del", "dev", device, "root"}
	_, clearErr := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if clearErr != nil {
		log.Warn().Err(clearErr).Str("container", targetContainerID[:12]).Str("device", device).Msg("failed to clear tc rules before injection")
	}
}

// injectWholeDevice applies netem directly as the root qdisc — affects all traffic on the device.
func (tw *TCWrapper) injectWholeDevice(ctx context.Context, targetContainerID string, params FaultParams) error {
	device := params.Device
	if device == "" {
		device = "eth0"
	}

	cmd := []string{"tc", "qdisc", "add", "dev", device, "root", "netem"}
	cmd = appendNetemParams(cmd, params)

	fmt.Printf("Injecting fault on target %s: %s\n", targetContainerID[:12], strings.Join(cmd, " "))

	output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to inject network fault: %w (output: %s)", err, output)
	}

	fmt.Printf("Fault injected successfully on target %s\n", targetContainerID[:12])
	return nil
}

// injectWithPortFilter uses a prio qdisc + u32 filters for port-specific faults.
// This works purely at the tc level without iptables dependency.
func (tw *TCWrapper) injectWithPortFilter(ctx context.Context, targetContainerID string, params FaultParams) error {
	device := params.Device
	if device == "" {
		device = "eth0"
	}

	// Step 1: Create a prio qdisc as root with 3 bands
	// Band 1 (1:1) = default traffic (no shaping), Band 2 (1:2) = shaped traffic
	prioCmd := []string{"tc", "qdisc", "add", "dev", device, "root", "handle", "1:", "prio", "bands", "3", "priomap",
		"0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0"}
	fmt.Printf("Injecting fault on target %s: setting up tc prio qdisc with u32 port filter\n", targetContainerID[:12])
	if output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, prioCmd); err != nil {
		return fmt.Errorf("failed to create prio qdisc: %w (output: %s)", err, output)
	}

	// Step 2: Attach netem to band 2 with fault parameters
	netemCmd := []string{"tc", "qdisc", "add", "dev", device, "parent", "1:2", "handle", "20:", "netem"}
	netemCmd = appendNetemParams(netemCmd, params)
	if output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, netemCmd); err != nil {
		return fmt.Errorf("failed to create netem qdisc: %w (output: %s)", err, output)
	}

	// Step 3: Add u32 filters to match traffic by port and direct to band 2
	ports := strings.Split(params.TargetPorts, ",")
	protos := parseProtos(params.TargetProto)

	for _, port := range ports {
		port = strings.TrimSpace(port)
		for _, proto := range protos {
			protoNum := "6" // tcp
			if proto == "udp" {
				protoNum = "17"
			}

			// Match destination port
			dportCmd := []string{"tc", "filter", "add", "dev", device, "parent", "1:0", "protocol", "ip",
				"u32", "match", "ip", "protocol", protoNum, "0xff",
				"match", "ip", "dport", port, "0xffff", "flowid", "1:2"}
			if output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, dportCmd); err != nil {
				return fmt.Errorf("failed to add dport filter for %s/%s: %w (output: %s)", proto, port, err, output)
			}

			// Match source port
			sportCmd := []string{"tc", "filter", "add", "dev", device, "parent", "1:0", "protocol", "ip",
				"u32", "match", "ip", "protocol", protoNum, "0xff",
				"match", "ip", "sport", port, "0xffff", "flowid", "1:2"}
			if output, err := tw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, sportCmd); err != nil {
				return fmt.Errorf("failed to add sport filter for %s/%s: %w (output: %s)", proto, port, err, output)
			}

			fmt.Printf("  → %s port %s filter added (sport + dport)\n", proto, port)
		}
	}

	fmt.Printf("Fault injected successfully on target %s (tc u32 port filter)\n", targetContainerID[:12])
	return nil
}

// appendNetemParams appends netem parameters (delay, loss, reorder) to a tc command
func appendNetemParams(cmd []string, params FaultParams) []string {
	if params.Latency > 0 {
		cmd = append(cmd, "delay", fmt.Sprintf("%dms", params.Latency))
	}
	if params.PacketLoss > 0 {
		cmd = append(cmd, "loss", fmt.Sprintf("%.2f%%", params.PacketLoss))
	}
	if params.Reorder > 0 {
		cmd = append(cmd, "reorder", fmt.Sprintf("%d%%", params.Reorder))
		if params.ReorderCorrelation > 0 {
			cmd = append(cmd, fmt.Sprintf("%d%%", params.ReorderCorrelation))
		}
	}
	if params.Corrupt > 0 {
		cmd = append(cmd, "corrupt", fmt.Sprintf("%.2f%%", params.Corrupt))
	}
	if params.Duplicate > 0 {
		cmd = append(cmd, "duplicate", fmt.Sprintf("%.2f%%", params.Duplicate))
	}
	if params.Bandwidth > 0 {
		cmd = append(cmd, "rate", fmt.Sprintf("%dkbit", params.Bandwidth))
	}
	return cmd
}

// parseProtos splits a comma-separated protocol string into individual protocols
func parseProtos(protoStr string) []string {
	if protoStr == "" {
		return []string{"tcp"}
	}
	var protos []string
	for _, p := range strings.Split(protoStr, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			protos = append(protos, p)
		}
	}
	if len(protos) == 0 {
		return []string{"tcp"}
	}
	return protos
}
