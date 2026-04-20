package cleanup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

// Coordinator orchestrates cleanup of all chaos artifacts
type Coordinator struct {
	sidecarMgr *sidecar.Manager
	auditLog   []AuditEntry
}

// AuditEntry represents a cleanup action
type AuditEntry struct {
	Timestamp   time.Time
	Action      string
	Target      string
	Success     bool
	Error       error
	Details     string
}

// New creates a new cleanup coordinator
func New(sidecarMgr *sidecar.Manager) *Coordinator {
	return &Coordinator{
		sidecarMgr: sidecarMgr,
		auditLog:   make([]AuditEntry, 0),
	}
}

// CleanupAll performs complete cleanup of all sidecars and verifies namespaces
func (c *Coordinator) CleanupAll(ctx context.Context) error {
	fmt.Println("🧹 Starting cleanup of all chaos artifacts...")

	sidecars := c.sidecarMgr.ListSidecars()
	totalSidecars := len(sidecars)

	if totalSidecars == 0 {
		fmt.Println("   No sidecars to clean up")
		return nil
	}

	fmt.Printf("   Found %d sidecar(s) to clean up\n", totalSidecars)

	errors := make([]error, 0)
	cleaned := 0
	failed := 0

	for targetID, sidecarID := range sidecars {
		fmt.Printf("   Cleaning target %s (sidecar %s)...\n", targetID[:12], sidecarID[:12])

		err := c.cleanupSidecar(ctx, targetID)
		if err != nil {
			fmt.Printf("   ❌ Failed to clean target %s: %v\n", targetID[:12], err)
			errors = append(errors, err)
			failed++
		} else {
			fmt.Printf("   ✅ Cleaned target %s\n", targetID[:12])
			cleaned++
		}
	}

	fmt.Printf("🧹 Cleanup complete: %d succeeded, %d failed\n", cleaned, failed)

	if len(errors) > 0 {
		errMsgs := make([]string, len(errors))
		for i, e := range errors {
			errMsgs[i] = e.Error()
		}
		return fmt.Errorf("cleanup completed with %d error(s):\n  - %s", len(errors), strings.Join(errMsgs, "\n  - "))
	}

	return nil
}

// cleanupSidecar cleans up a single sidecar and verifies the target namespace.
// Verification MUST happen before sidecar destruction because the sidecar shares
// the target's network namespace and has the tools (tc, iptables) needed to
// inspect and clean rules. Once the sidecar is destroyed, those tools and the
// shared namespace access are gone.
func (c *Coordinator) cleanupSidecar(ctx context.Context, targetID string) error {
	// Step 1: Verify namespace is clean via sidecar (before destruction)
	c.logAudit("verify_namespace", targetID, "Verifying target namespace via sidecar", nil)
	verifyClean := c.verifySidecarNamespace(ctx, targetID)

	if !verifyClean {
		// Step 2: Attempt cleanup via sidecar
		c.logAudit("cleanup_artifacts", targetID, "Cleaning remaining tc/iptables rules via sidecar", nil)
		c.cleanViaSidecar(ctx, targetID)

		// Step 3: Re-verify
		if !c.verifySidecarNamespace(ctx, targetID) {
			c.logAudit("verify_namespace", targetID, "Namespace still has rules after cleanup — proceeding with sidecar destruction", nil)
		} else {
			c.logAudit("verify_namespace", targetID, "Namespace clean after cleanup", nil)
		}
	} else {
		c.logAudit("verify_namespace", targetID, "Namespace is clean", nil)
	}

	// Step 4: Destroy sidecar (always, even if verification had issues)
	c.logAudit("destroy_sidecar", targetID, "Destroying sidecar container", nil)
	err := c.sidecarMgr.DestroySidecar(ctx, targetID)
	if err != nil {
		c.logAudit("destroy_sidecar", targetID, "Failed to destroy sidecar", err)
		return fmt.Errorf("failed to destroy sidecar: %w", err)
	}
	c.logAudit("destroy_sidecar", targetID, "Sidecar destroyed successfully", nil)

	return nil
}

// verifySidecarNamespace checks tc and iptables rules via the sidecar.
// Returns true if the namespace is clean (no netem/tbf/chaos rules).
func (c *Coordinator) verifySidecarNamespace(ctx context.Context, targetID string) bool {
	clean := true

	// Check tc rules
	output, err := c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"tc", "qdisc", "show", "dev", "eth0"})
	if err != nil {
		// Sidecar may already be gone — treat as clean (best-effort)
		return true
	}
	if strings.Contains(output, "netem") || strings.Contains(output, "tbf") {
		c.logAudit("verify_namespace", targetID, fmt.Sprintf("TC rules still present: %s", strings.TrimSpace(output)), nil)
		clean = false
	}

	// Check iptables rules (filter + nat tables).
	output, err = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"iptables", "-L", "-n"})
	if err == nil && (strings.Contains(output, "CHAOS_DROP") || strings.Contains(output, "chaos-engineering")) {
		c.logAudit("verify_namespace", targetID, "iptables CHAOS_DROP rules still present", nil)
		clean = false
	}
	natOutput, natErr := c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"iptables", "-t", "nat", "-L", "-n"})
	if natErr == nil && (strings.Contains(natOutput, "chaos-http-fault") || strings.Contains(natOutput, "chaos-corruption-proxy")) {
		c.logAudit("verify_namespace", targetID, "iptables PREROUTING redirect rules still present", nil)
		clean = false
	}

	return clean
}

// cleanViaSidecar removes tc and iptables rules using the sidecar.
func (c *Coordinator) cleanViaSidecar(ctx context.Context, targetID string) {
	// Remove tc qdisc (covers all tc-based faults)
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"tc", "qdisc", "del", "dev", "eth0", "root"})

	// Remove firewall CHAOS_DROP chain and INPUT jump (connection_drop fault).
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"iptables", "-D", "INPUT", "-j", "CHAOS_DROP", "-m", "comment", "--comment", "chaos-engineering"})
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"iptables", "-F", "CHAOS_DROP"})
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"iptables", "-X", "CHAOS_DROP"})

	// Remove HTTP-fault and corruption-proxy PREROUTING redirects. Walk
	// iptables-save so each install's exact rule spec is matched without
	// needing to remember every target port.
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"sh", "-c",
		"iptables-save -t nat 2>/dev/null | grep -E 'chaos-(http-fault|corruption-proxy)' | " +
			"sed 's/^-A /-D /' | while IFS= read -r rule; do iptables -t nat $rule 2>/dev/null; done; true",
	})

	// Remove clock-skew NTP-block rule (installed by time.ClockSkewWrapper
	// when disable_ntp=true).
	_, _ = c.sidecarMgr.ExecInSidecar(ctx, targetID, []string{"sh", "-c",
		"iptables -D OUTPUT -p udp --dport 123 -j DROP -m comment --comment chaos-ntp-block 2>/dev/null || true",
	})
}

// logAudit adds an entry to the audit log
func (c *Coordinator) logAudit(action, target, details string, err error) {
	entry := AuditEntry{
		Timestamp: time.Now(),
		Action:    action,
		Target:    target,
		Success:   err == nil,
		Error:     err,
		Details:   details,
	}
	c.auditLog = append(c.auditLog, entry)
}

// GetAuditLog returns the complete audit log
func (c *Coordinator) GetAuditLog() []AuditEntry {
	return c.auditLog
}

// PrintAuditLog prints the audit log in a readable format
func (c *Coordinator) PrintAuditLog() {
	if len(c.auditLog) == 0 {
		fmt.Println("No cleanup actions logged")
		return
	}

	fmt.Println("\n📋 Cleanup Audit Log:")
	fmt.Println("─────────────────────────────────────────────────────────────")

	for i, entry := range c.auditLog {
		status := "✅"
		if !entry.Success {
			status = "❌"
		}

		fmt.Printf("%d. [%s] %s %s\n", i+1, entry.Timestamp.Format("15:04:05"), status, entry.Action)
		fmt.Printf("   Target: %s\n", entry.Target)
		fmt.Printf("   Details: %s\n", entry.Details)

		if entry.Error != nil {
			fmt.Printf("   Error: %v\n", entry.Error)
		}
		fmt.Println()
	}

	fmt.Println("─────────────────────────────────────────────────────────────")
}

// GetSummary returns a summary of cleanup actions
func (c *Coordinator) GetSummary() CleanupSummary {
	summary := CleanupSummary{
		TotalActions: len(c.auditLog),
		Succeeded:    0,
		Failed:       0,
	}

	for _, entry := range c.auditLog {
		if entry.Success {
			summary.Succeeded++
		} else {
			summary.Failed++
		}
	}

	return summary
}

// CleanupSummary contains summary statistics
type CleanupSummary struct {
	TotalActions int
	Succeeded    int
	Failed       int
}

// String returns a string representation of the summary
func (s CleanupSummary) String() string {
	return fmt.Sprintf("Cleanup Summary: %d total actions, %d succeeded, %d failed",
		s.TotalActions, s.Succeeded, s.Failed)
}
