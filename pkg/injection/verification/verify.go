package verification

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// Verifier checks for remaining chaos artifacts in container network namespaces
type Verifier struct {
	dockerClient *docker.Client
}

// New creates a new verifier
func New(dockerClient *docker.Client) *Verifier {
	return &Verifier{
		dockerClient: dockerClient,
	}
}

// VerificationResult contains the results of a namespace verification check
type VerificationResult struct {
	ContainerID    string
	Clean          bool
	TCRulesFound   bool
	IPTablesFound  bool
	NFTablesFound  bool
	EnvoyFound     bool
	Details        []string
}

// VerifyNamespaceClean checks if a container's network namespace is clean
func (v *Verifier) VerifyNamespaceClean(ctx context.Context, containerID string) (*VerificationResult, error) {
	result := &VerificationResult{
		ContainerID: containerID,
		Clean:       true,
		Details:     make([]string, 0),
	}

	// Get container PID
	pid, err := v.dockerClient.GetContainerPID(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get container PID: %w", err)
	}

	// Check tc (traffic control) rules
	if hasTC, details := v.checkTCRules(ctx, containerID, pid); hasTC {
		result.TCRulesFound = true
		result.Clean = false
		result.Details = append(result.Details, details...)
	}

	// Check iptables rules
	if hasIPTables, details := v.checkIPTablesRules(ctx, containerID, pid); hasIPTables {
		result.IPTablesFound = true
		result.Clean = false
		result.Details = append(result.Details, details...)
	}

	// Check nftables rules
	if hasNFTables, details := v.checkNFTablesRules(ctx, containerID, pid); hasNFTables {
		result.NFTablesFound = true
		result.Clean = false
		result.Details = append(result.Details, details...)
	}

	// Check for Envoy processes (if L7 faults were used)
	if hasEnvoy, details := v.checkEnvoyProcesses(ctx, containerID); hasEnvoy {
		result.EnvoyFound = true
		result.Clean = false
		result.Details = append(result.Details, details...)
	}

	return result, nil
}

// checkTCRules checks for traffic control rules using tc command
func (v *Verifier) checkTCRules(ctx context.Context, containerID string, pid int) (bool, []string) {
	// Use nsenter to check tc rules in the container's network namespace
	cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "tc", "qdisc", "show"}

	output, err := v.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		// If command fails, assume no rules
		return false, nil
	}

	// Check if output contains netem or tbf (traffic shaping) qdiscs
	if strings.Contains(output, "netem") || strings.Contains(output, "tbf") {
		return true, []string{fmt.Sprintf("TC rules found: %s", output)}
	}

	return false, nil
}

// checkIPTablesRules checks for iptables rules
func (v *Verifier) checkIPTablesRules(ctx context.Context, containerID string, pid int) (bool, []string) {
	// Use nsenter to check iptables rules
	cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "iptables", "-L", "-n"}

	output, err := v.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		// If command fails, assume no rules
		return false, nil
	}

	// Check if output contains chaos_utils chains or rules
	if strings.Contains(output, "chaos_utils") || strings.Contains(output, "CHAOS") {
		return true, []string{fmt.Sprintf("iptables rules found: %s", output)}
	}

	return false, nil
}

// checkNFTablesRules checks for nftables rules
func (v *Verifier) checkNFTablesRules(ctx context.Context, containerID string, pid int) (bool, []string) {
	// Use nsenter to check nftables rules
	cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "nft", "list", "tables"}

	output, err := v.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		// If command fails, assume no rules (nft may not be available)
		return false, nil
	}

	// Check if output contains chaos_utils table
	if strings.Contains(output, "chaos_utils") {
		return true, []string{fmt.Sprintf("nftables rules found: %s", output)}
	}

	return false, nil
}

// checkEnvoyProcesses checks for running Envoy processes
func (v *Verifier) checkEnvoyProcesses(ctx context.Context, containerID string) (bool, []string) {
	// Check if any Envoy processes are running in the container
	cmd := []string{"ps", "aux"}

	output, err := v.dockerClient.ExecCommand(ctx, containerID, cmd)
	if err != nil {
		// If command fails, assume no processes
		return false, nil
	}

	// Check if output contains envoy processes
	if strings.Contains(output, "envoy") {
		return true, []string{fmt.Sprintf("Envoy process found: %s", output)}
	}

	return false, nil
}

// CleanupArtifacts attempts to clean up any remaining chaos artifacts
func (v *Verifier) CleanupArtifacts(ctx context.Context, containerID string) error {
	result, err := v.VerifyNamespaceClean(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to verify namespace: %w", err)
	}

	if result.Clean {
		// Already clean
		return nil
	}

	pid, err := v.dockerClient.GetContainerPID(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to get container PID: %w", err)
	}

	// Try to clean up TC rules
	if result.TCRulesFound {
		cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "tc", "qdisc", "del", "dev", "eth0", "root"}
		_, _ = v.dockerClient.ExecCommand(ctx, containerID, cmd) // Ignore errors
	}

	// Try to clean up iptables rules
	if result.IPTablesFound {
		// Flush chaos_utils chain if it exists
		cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "iptables", "-F", "chaos_utils"}
		_, _ = v.dockerClient.ExecCommand(ctx, containerID, cmd) // Ignore errors
	}

	// Try to clean up nftables rules
	if result.NFTablesFound {
		cmd := []string{"nsenter", "-t", fmt.Sprintf("%d", pid), "-n", "nft", "delete", "table", "chaos_utils"}
		_, _ = v.dockerClient.ExecCommand(ctx, containerID, cmd) // Ignore errors
	}

	// Kill Envoy processes
	if result.EnvoyFound {
		cmd := []string{"pkill", "envoy"}
		_, _ = v.dockerClient.ExecCommand(ctx, containerID, cmd) // Ignore errors
	}

	// Verify cleanup worked
	verifyResult, err := v.VerifyNamespaceClean(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to verify cleanup: %w", err)
	}

	if !verifyResult.Clean {
		return fmt.Errorf("cleanup incomplete: %v", verifyResult.Details)
	}

	return nil
}
