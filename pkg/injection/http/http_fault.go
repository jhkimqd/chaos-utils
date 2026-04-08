package http

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
)

// HTTPFaultParams defines parameters for HTTP fault injection
type HTTPFaultParams struct {
	// TargetPort is the HTTP port to intercept (e.g., 8545, 1317, 26657)
	TargetPort int

	// DelayMs adds latency to HTTP responses (milliseconds)
	DelayMs int

	// DelayPercent is the percentage of requests to delay (0-100, default: 100)
	DelayPercent int

	// AbortCode is the HTTP status code to return (e.g., 500, 503, 429)
	AbortCode int

	// AbortPercent is the percentage of requests to abort (0-100, default: 100)
	AbortPercent int

	// BodyOverride replaces the response body with this content
	BodyOverride string

	// HeaderOverrides adds or replaces response headers (key → value)
	HeaderOverrides map[string]string

	// PathPattern restricts faults to requests matching this path
	// Prefix match by default, use "~<regex>" for regex match
	PathPattern string
}

// proxyPort returns the Envoy proxy listen port (offset from target)
func (p HTTPFaultParams) proxyPort() int {
	// Use a deterministic proxy port based on target port to avoid collisions
	return 15000 + p.TargetPort
}

// SidecarManager interface for sidecar operations
type SidecarManager interface {
	CreateSidecar(ctx context.Context, targetContainerID string) (string, error)
	ExecInSidecar(ctx context.Context, targetContainerID string, cmd []string) (string, error)
	GetSidecarID(targetContainerID string) (string, bool)
}

// HTTPFaultWrapper manages HTTP fault injection via Envoy proxy
type HTTPFaultWrapper struct {
	sidecarMgr    SidecarManager
	mu            sync.Mutex
	injectedPorts map[string][]int // tracks target ports per container for cleanup
}

// New creates a new HTTP fault wrapper
func New(sidecarMgr SidecarManager) *HTTPFaultWrapper {
	return &HTTPFaultWrapper{
		sidecarMgr:    sidecarMgr,
		injectedPorts: make(map[string][]int),
	}
}

// InjectHTTPFault sets up Envoy as a transparent HTTP proxy with fault injection.
//
// Steps:
//  1. Create sidecar (shares target's network namespace)
//  2. Write Envoy config to sidecar
//  3. Start Envoy in background
//  4. Set up iptables REDIRECT to route traffic through Envoy
func (hw *HTTPFaultWrapper) InjectHTTPFault(ctx context.Context, targetContainerID string, params HTTPFaultParams) error {
	// Ensure sidecar exists
	if _, exists := hw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		fmt.Printf("Creating sidecar for HTTP fault injection on target %s\n", targetContainerID[:12])
		if _, err := hw.sidecarMgr.CreateSidecar(ctx, targetContainerID); err != nil {
			return fmt.Errorf("failed to create sidecar: %w", err)
		}
	}

	proxyPort := params.proxyPort()
	configPath := fmt.Sprintf("/tmp/envoy-chaos-%d.yaml", params.TargetPort)

	// Step 1: Generate and write Envoy config
	config := generateEnvoyConfig(params)
	encoded := base64.StdEncoding.EncodeToString([]byte(config))
	writeCmd := []string{"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", encoded, configPath)}

	fmt.Printf("Writing Envoy config for port %d on target %s\n", params.TargetPort, targetContainerID[:12])
	output, err := hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write Envoy config: %w (output: %s)", err, output)
	}

	// Step 2: Start Envoy in background
	envoyCmd := []string{"sh", "-c", fmt.Sprintf(
		"envoy -c %s --log-level warn --log-path /tmp/envoy-chaos-%d.log &",
		configPath, params.TargetPort,
	)}

	fmt.Printf("Starting Envoy proxy on port %d for target %s\n", proxyPort, targetContainerID[:12])
	output, err = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, envoyCmd)
	if err != nil {
		return fmt.Errorf("failed to start Envoy: %w (output: %s)", err, output)
	}

	// Step 3: Wait for Envoy to be ready
	readyCmd := []string{"sh", "-c", fmt.Sprintf(
		"for i in $(seq 1 30); do curl -s http://127.0.0.1:%d/ready > /dev/null 2>&1 && exit 0; sleep 0.5; done; exit 1",
		proxyPort+1, // admin port
	)}

	output, err = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, readyCmd)
	if err != nil {
		// Try to get Envoy logs for debugging
		logCmd := []string{"sh", "-c", fmt.Sprintf("tail -20 /tmp/envoy-chaos-%d.log 2>/dev/null || echo 'no logs'", params.TargetPort)}
		logs, _ := hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, logCmd)
		return fmt.Errorf("envoy failed to start within 15s: %w (logs: %s)", err, logs)
	}

	// Step 4: Set up iptables REDIRECT in PREROUTING only.
	// PREROUTING intercepts inbound traffic from other containers before it
	// reaches the target service. SO_ORIGINAL_DST is preserved for Envoy's
	// original_dst listener filter.
	// We do NOT redirect in OUTPUT — that would intercept Envoy's own outbound
	// connections to the real service, creating an infinite loop.
	iptablesCmds := [][]string{
		// Intercept inbound traffic to target port → redirect to Envoy
		{"iptables", "-t", "nat", "-A", "PREROUTING",
			"-p", "tcp", "--dport", fmt.Sprintf("%d", params.TargetPort),
			"-j", "REDIRECT", "--to-port", fmt.Sprintf("%d", proxyPort),
			"-m", "comment", "--comment", "chaos-http-fault"},
	}

	for _, cmd := range iptablesCmds {
		fmt.Printf("  iptables: %s\n", strings.Join(cmd, " "))
		output, err = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
		if err != nil {
			// Log but don't fail for uid-owner rule (envoy user may not exist)
			fmt.Printf("  Warning: iptables command failed: %v (output: %s)\n", err, output)
		}
	}

	// Track for cleanup
	hw.mu.Lock()
	hw.injectedPorts[targetContainerID] = append(hw.injectedPorts[targetContainerID], params.TargetPort)
	hw.mu.Unlock()

	fmt.Printf("HTTP fault injection active on target %s (port %d → Envoy:%d)\n",
		targetContainerID[:12], params.TargetPort, proxyPort)

	if params.DelayMs > 0 {
		fmt.Printf("  Delay: %dms (%d%% of requests)\n", params.DelayMs, params.DelayPercent)
	}
	if params.AbortCode > 0 {
		fmt.Printf("  Abort: HTTP %d (%d%% of requests)\n", params.AbortCode, params.AbortPercent)
	}
	if params.BodyOverride != "" {
		fmt.Printf("  Body override: %s\n", truncate(params.BodyOverride, 80))
	}
	if len(params.HeaderOverrides) > 0 {
		fmt.Printf("  Header overrides: %v\n", params.HeaderOverrides)
	}

	return nil
}

// RemoveFault stops Envoy and removes iptables redirect rules
func (hw *HTTPFaultWrapper) RemoveFault(ctx context.Context, targetContainerID string, params HTTPFaultParams) error {
	if _, exists := hw.sidecarMgr.GetSidecarID(targetContainerID); !exists {
		return fmt.Errorf("no sidecar found for target %s", targetContainerID)
	}

	fmt.Printf("Removing HTTP fault injection from target %s (port %d)\n",
		targetContainerID[:12], params.TargetPort)

	// Remove iptables PREROUTING redirect rule
	cleanupCmds := [][]string{
		{"sh", "-c", fmt.Sprintf(
			"iptables -t nat -D PREROUTING -p tcp --dport %d -j REDIRECT --to-port %d -m comment --comment chaos-http-fault 2>/dev/null || true",
			params.TargetPort, params.proxyPort())},
	}

	for _, cmd := range cleanupCmds {
		_, _ = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, cmd)
	}

	// Stop Envoy using /proc scanning (sidecar is Ubuntu so pkill exists, but
	// use /proc for consistency). Match by config file path to target the right instance.
	killCmd := []string{"sh", "-c", fmt.Sprintf(
		"for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); "+
			"if tr '\\0' ' ' < $p 2>/dev/null | grep -q 'envoy-chaos-%d'; then kill -9 $PID 2>/dev/null; fi; done; "+
			"rm -f /tmp/envoy-chaos-%d.yaml /tmp/envoy-chaos-%d.log",
		params.TargetPort, params.TargetPort, params.TargetPort)}
	_, _ = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, killCmd)

	fmt.Printf("HTTP fault injection removed from target %s\n", targetContainerID[:12])
	return nil
}

// RemoveAllFaults removes all HTTP faults for a container (used by injector cleanup)
func (hw *HTTPFaultWrapper) RemoveAllFaults(ctx context.Context, targetContainerID string) error {
	hw.mu.Lock()
	ports, exists := hw.injectedPorts[targetContainerID]
	hw.mu.Unlock()
	if !exists || len(ports) == 0 {
		// Fallback: kill envoy and remove only chaos-http-fault iptables rules
		if _, ok := hw.sidecarMgr.GetSidecarID(targetContainerID); ok {
			killCmd := []string{"sh", "-c",
				"for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); " +
					"if tr '\\0' ' ' < $p 2>/dev/null | grep -q envoy; then kill -9 $PID 2>/dev/null; fi; done; " +
					"while iptables -t nat -D PREROUTING -m comment --comment chaos-http-fault -j REDIRECT 2>/dev/null; do true; done; " +
					"rm -f /tmp/envoy-chaos-*.yaml /tmp/envoy-chaos-*.log 2>/dev/null; " +
					"echo done"}
			_, _ = hw.sidecarMgr.ExecInSidecar(ctx, targetContainerID, killCmd)
		}
		return nil
	}

	for _, port := range ports {
		_ = hw.RemoveFault(ctx, targetContainerID, HTTPFaultParams{TargetPort: port})
	}

	hw.mu.Lock()
	delete(hw.injectedPorts, targetContainerID)
	hw.mu.Unlock()
	return nil
}

// truncate shortens a string with ellipsis
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// ValidateHTTPFaultParams validates HTTP fault parameters
func ValidateHTTPFaultParams(params HTTPFaultParams) error {
	if params.TargetPort <= 0 || params.TargetPort > 65535 {
		return fmt.Errorf("target_port must be between 1 and 65535")
	}

	if params.DelayMs < 0 {
		return fmt.Errorf("delay_ms cannot be negative")
	}

	if params.DelayPercent < 0 || params.DelayPercent > 100 {
		return fmt.Errorf("delay_percent must be between 0 and 100")
	}

	if params.AbortCode < 0 || params.AbortCode > 599 {
		return fmt.Errorf("abort_code must be a valid HTTP status code")
	}

	if params.AbortPercent < 0 || params.AbortPercent > 100 {
		return fmt.Errorf("abort_percent must be between 0 and 100")
	}

	// Must specify at least one fault action
	if params.DelayMs == 0 && params.AbortCode == 0 && params.BodyOverride == "" && len(params.HeaderOverrides) == 0 {
		return fmt.Errorf("at least one fault action must be specified (delay_ms, abort_code, body_override, or header_overrides)")
	}

	return nil
}
