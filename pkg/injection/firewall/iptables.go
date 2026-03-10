package firewall

import (
	"context"
	"fmt"
	"strings"
)

// ConnectionDropParams defines parameters for connection drop injection
type ConnectionDropParams struct {
	// RuleType is the action to take: "drop" (silent) or "reject" (send RST)
	RuleType string

	// TargetPorts is comma-separated list of ports (e.g., "26656,26657")
	TargetPorts string

	// TargetProto is the protocol: "tcp", "udp", or "tcp,udp"
	TargetProto string

	// Probability is the drop rate (0.0-1.0, e.g., 0.1 = 10%)
	Probability float64

	// Stateful tracks connection state if true
	Stateful bool
}

// IptablesWrapper wraps iptables for connection manipulation
type IptablesWrapper struct {
	sidecarMgr SidecarManager
}

// SidecarManager interface for sidecar operations
type SidecarManager interface {
	CreateSidecar(ctx context.Context, targetContainerID string) (string, error)
	ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error)
	GetSidecarID(targetContainerID string) (string, bool)
}

// New creates a new iptables wrapper
func New(sidecarMgr SidecarManager) *IptablesWrapper {
	return &IptablesWrapper{
		sidecarMgr: sidecarMgr,
	}
}

// InjectConnectionDrop injects connection drop rules
func (iw *IptablesWrapper) InjectConnectionDrop(ctx context.Context, targetContainerID string, params ConnectionDropParams) error {
	// Ensure sidecar exists
	if _, exists := iw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for target %s\n", targetContainerID[:12])
		if _, err := iw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	// Build iptables commands
	cmds, err := iw.buildIptablesCommands(params)
	if err != nil {
		return fmt.Errorf("failed to build iptables commands: %w", err)
	}

	fmt.Printf("Injecting connection drop on target %s\n", targetContainerID[:12])

	// Execute each command
	for _, cmd := range cmds {
		fmt.Printf("  Executing: %s\n", strings.Join(cmd, " "))
		output, err := iw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
		if err != nil {
			return fmt.Errorf("failed to inject connection drop: %w (output: %s)", err, output)
		}
	}

	fmt.Printf("Connection drop rules injected successfully on target %s\n", targetContainerID[:12])

	return nil
}

// RemoveFault removes all connection drop rules
func (iw *IptablesWrapper) RemoveFault(ctx context.Context, targetContainerID string) error {
	if _, exists := iw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing connection drop rules from target %s\n", targetContainerID[:12])

	// Flush all rules with our custom chain marker
	flushCmds := [][]string{
		{"iptables", "-D", "INPUT", "-j", "CHAOS_DROP", "-m", "comment", "--comment", "chaos-engineering"},
		{"iptables", "-F", "CHAOS_DROP"},
		{"iptables", "-X", "CHAOS_DROP"},
	}

	for _, cmd := range flushCmds {
		// Ignore errors - chain might not exist
		_, _ = iw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	}

	fmt.Printf("Connection drop rules removed successfully from target %s\n", targetContainerID[:12])

	return nil
}

// buildIptablesCommands builds iptables commands for connection dropping
func (iw *IptablesWrapper) buildIptablesCommands(params ConnectionDropParams) ([][]string, error) {
	var cmds [][]string

	// Create custom chain for chaos rules
	cmds = append(cmds, []string{"iptables", "-N", "CHAOS_DROP"})

	// Split protocols
	protocols := []string{"tcp"}
	if params.TargetProto != "" {
		protocols = strings.Split(params.TargetProto, ",")
	}

	// Split ports
	ports := strings.Split(params.TargetPorts, ",")

	// Build rules for each protocol and port.
	// We add both --dport and --sport rules because P2P connections can be
	// initiated from either side. If this node initiated the connection (using a
	// random source port to connect to the remote's 30303), incoming responses
	// arrive with sport=30303 but a random dport — so --dport alone misses them.
	for _, proto := range protocols {
		proto = strings.TrimSpace(proto)
		for _, port := range ports {
			port = strings.TrimSpace(port)

			// Match destination port (catches connections accepted on this port)
			dportRule := iw.buildDropRule(proto, "--dport", port, params)
			cmds = append(cmds, dportRule)

			// Match source port (catches return traffic from connections this node initiated)
			if port != "" {
				sportRule := iw.buildDropRule(proto, "--sport", port, params)
				cmds = append(cmds, sportRule)
			}
		}
	}

	// Jump to custom chain from INPUT
	cmds = append(cmds, []string{
		"iptables", "-A", "INPUT", "-j", "CHAOS_DROP",
		"-m", "comment", "--comment", "chaos-engineering",
	})

	return cmds, nil
}

// buildDropRule builds a single iptables drop/reject rule
func (iw *IptablesWrapper) buildDropRule(proto, portFlag, port string, params ConnectionDropParams) []string {
	rule := []string{"iptables", "-A", "CHAOS_DROP", "-p", proto}

	if port != "" {
		rule = append(rule, portFlag, port)
	}

	if params.Probability > 0 {
		rule = append(rule,
			"-m", "statistic",
			"--mode", "random",
			"--probability", fmt.Sprintf("%.4f", params.Probability),
		)
	}

	action := "DROP"
	if params.RuleType == "reject" {
		action = "REJECT"
		if proto == "tcp" {
			rule = append(rule, "-j", action, "--reject-with", "tcp-reset")
		} else {
			rule = append(rule, "-j", action, "--reject-with", "icmp-port-unreachable")
		}
	} else {
		rule = append(rule, "-j", action)
	}

	return rule
}

// ValidateConnectionDropParams validates connection drop parameters
func ValidateConnectionDropParams(params ConnectionDropParams) error {
	if params.RuleType != "drop" && params.RuleType != "reject" {
		return fmt.Errorf("rule_type must be 'drop' or 'reject'")
	}

	if params.Probability < 0 || params.Probability > 1.0 {
		return fmt.Errorf("probability must be between 0.0 and 1.0")
	}

	// TargetPorts is optional — empty means all ports.

	return nil
}
