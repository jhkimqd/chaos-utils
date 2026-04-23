package dns

import (
	"context"
	"fmt"
	"strings"
)

// DNSParams defines parameters for DNS delay injection
type DNSParams struct {
	// DelayMs is the delay in milliseconds
	DelayMs int

	// FailureRate is the probability of DNS query failure (0.0-1.0)
	FailureRate float64

	// Method is the injection method: "dnsmasq" or "hosts"
	Method string
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

// InjectDNSDelay injects DNS resolution delays
func (dw *DNSWrapper) InjectDNSDelay(ctx context.Context, targetContainerID string, params DNSParams) error {
	// Ensure sidecar exists
	if _, exists := dw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := dw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	fmt.Printf("Injecting DNS delay on target %s\n", targetContainerID[:12])

	// Use tc to delay DNS traffic (port 53)
	// This is simpler and more reliable than setting up dnsmasq
	cmds := [][]string{
		// Add latency to DNS traffic (UDP port 53)
		{"tc", "qdisc", "add", "dev", "eth0", "root", "handle", "1:", "prio"},
		{"tc", "qdisc", "add", "dev", "eth0", "parent", "1:3", "handle", "30:", "netem",
			"delay", fmt.Sprintf("%dms", params.DelayMs)},
		{"tc", "filter", "add", "dev", "eth0", "protocol", "ip", "parent", "1:0",
			"prio", "3", "u32", "match", "ip", "dport", "53", "0xffff", "flowid", "1:3"},
	}

	// Add packet loss if failure rate specified
	if params.FailureRate > 0 {
		lossPercent := int(params.FailureRate * 100)
		cmds = append(cmds, []string{
			"tc", "qdisc", "change", "dev", "eth0", "parent", "1:3", "handle", "30:",
			"netem", "delay", fmt.Sprintf("%dms", params.DelayMs),
			"loss", fmt.Sprintf("%d%%", lossPercent),
		})
	}

	for _, cmd := range cmds {
		fmt.Printf("  Executing: %s\n", strings.Join(cmd, " "))
		output, err := dw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
		if err != nil {
			// If error is "File exists", it's fine - rule already exists
			if !strings.Contains(output, "File exists") {
				return fmt.Errorf("failed to inject DNS delay: %w (output: %s)", err, output)
			}
		}
	}

	fmt.Printf("DNS delay injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes DNS delay injection
func (dw *DNSWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	if _, exists := dw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing DNS delay from target %s\n", targetContainerID[:12])

	// Remove tc rules
	cleanupCmds := [][]string{
		{"tc", "qdisc", "del", "dev", "eth0", "root"},
	}

	for _, cmd := range cleanupCmds {
		// Ignore errors - rules might not exist
		_, _ = dw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
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
