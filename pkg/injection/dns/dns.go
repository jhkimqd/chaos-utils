package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// DNSParams defines parameters for DNS delay injection.
// Implementation applies tc netem on UDP/53 — other mechanisms are not
// implemented.
type DNSParams struct {
	// DelayMs is the delay in milliseconds
	DelayMs int

	// FailureRate is the probability of DNS query failure (0.0-1.0)
	FailureRate float64
}

// DNSWrapper wraps DNS manipulation via sidecars
type DNSWrapper struct {
	sidecarMgr SidecarManager
}

// SidecarManager interface for sidecar operations
type SidecarManager interface {
	CreateSidecar(ctx context.Context, targetContainerID string) (string, error)
	ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error)
	GetSidecarID(targetContainerID string) (string, bool)
}

// New creates a new DNS wrapper
func New(sidecarMgr SidecarManager) *DNSWrapper {
	return &DNSWrapper{
		sidecarMgr: sidecarMgr,
	}
}

// InjectDNSDelay injects DNS resolution delays via tc netem on UDP/53.
// If a prio root qdisc is already installed (e.g. by a coexisting network
// fault with port filtering), this reuses band 3 for the DNS-specific delay.
func (dw *DNSWrapper) InjectDNSDelay(ctx context.Context, targetContainerID string, params DNSParams) error {
	if _, exists := dw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := dw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	fmt.Printf("Injecting DNS delay on target %s\n", targetContainerID[:12])

	// Detect an existing root qdisc. The tc_wrapper may have already installed
	// a prio root (for port-filtered faults) or a netem root (for whole-device
	// faults). Refuse to clobber a netem root — that would silently wipe the
	// other fault.
	qdiscOut, _ := dw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, []string{"tc", "qdisc", "show", "dev", "eth0"})
	hasPrioRoot := strings.Contains(qdiscOut, "qdisc prio 1:")
	hasNetemRoot := strings.Contains(qdiscOut, "qdisc netem") && strings.Contains(qdiscOut, "root")

	if hasNetemRoot {
		return fmt.Errorf("cannot inject DNS delay on %s: a root netem qdisc is already installed — remove the other network fault first", targetContainerID[:12])
	}

	var cmds [][]string
	if !hasPrioRoot {
		cmds = append(cmds, []string{"tc", "qdisc", "add", "dev", "eth0", "root", "handle", "1:", "prio"})
	}

	netemArgs := []string{"tc", "qdisc", "add", "dev", "eth0", "parent", "1:3", "handle", "30:", "netem",
		"delay", fmt.Sprintf("%dms", params.DelayMs)}
	if params.FailureRate > 0 {
		lossPercent := int(params.FailureRate * 100)
		netemArgs = append(netemArgs, "loss", fmt.Sprintf("%d%%", lossPercent))
	}
	cmds = append(cmds, netemArgs)
	cmds = append(cmds, []string{"tc", "filter", "add", "dev", "eth0", "protocol", "ip", "parent", "1:0",
		"prio", "3", "u32", "match", "ip", "dport", "53", "0xffff", "flowid", "1:3"})

	for _, cmd := range cmds {
		fmt.Printf("  Executing: %s\n", strings.Join(cmd, " "))
		output, err := dw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
		if err != nil {
			return fmt.Errorf("failed to inject DNS delay: %w (output: %s)", err, output)
		}
	}

	fmt.Printf("DNS delay injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes DNS delay injection.
// Only the DNS-specific band 3 netem is removed; if no other network fault
// shares the prio root, the root qdisc is also deleted to fully clean state.
func (dw *DNSWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	if _, exists := dw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing DNS delay from target %s\n", targetContainerID[:12])

	// Remove only the DNS netem on band 3 and the u32 filter. Leave the prio
	// root alone in case another fault is still using bands 1/2.
	cleanupCmds := [][]string{
		{"tc", "qdisc", "del", "dev", "eth0", "parent", "1:3", "handle", "30:"},
		{"tc", "filter", "del", "dev", "eth0", "protocol", "ip", "parent", "1:0", "prio", "3"},
	}

	for _, cmd := range cleanupCmds {
		_, tcErr := dw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
		if tcErr != nil {
			log.Warn().Err(tcErr).Str("container", targetContainerID[:12]).Strs("cmd", cmd).Msg("failed to remove tc rule during DNS delay removal")
		}
	}

	fmt.Printf("DNS delay removed successfully from target %s\n", targetContainerID[:12])

	return nil
}

// ValidateDNSParams validates DNS parameters
func ValidateDNSParams(params DNSParams) error {
	if params.DelayMs < 0 {
		return fmt.Errorf("delay_ms cannot be negative")
	}

	if params.FailureRate < 0 || params.FailureRate > 1.0 {
		return fmt.Errorf("failure_rate must be between 0.0 and 1.0")
	}

	return nil
}
