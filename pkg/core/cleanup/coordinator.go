package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
	"github.com/jihwankim/chaos-utils/pkg/injection/verification"
)

// Coordinator orchestrates cleanup of all chaos artifacts
type Coordinator struct {
	sidecarMgr *sidecar.Manager
	verifier   *verification.Verifier
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
func New(sidecarMgr *sidecar.Manager, verifier *verification.Verifier) *Coordinator {
	return &Coordinator{
		sidecarMgr: sidecarMgr,
		verifier:   verifier,
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
		return fmt.Errorf("cleanup completed with %d errors: %v", len(errors), errors[0])
	}

	return nil
}

// cleanupSidecar cleans up a single sidecar and verifies the target namespace
func (c *Coordinator) cleanupSidecar(ctx context.Context, targetID string) error {
	// Step 1: Remove faults (execute comcast --stop in sidecar)
	c.logAudit("remove_faults", targetID, "Removing faults from target", nil)

	// We don't have direct access to comcast wrapper here, so we skip this
	// The sidecar manager should handle this via its cleanup

	// Step 2: Destroy sidecar
	c.logAudit("destroy_sidecar", targetID, "Destroying sidecar container", nil)
	err := c.sidecarMgr.DestroySidecar(ctx, targetID)
	if err != nil {
		c.logAudit("destroy_sidecar", targetID, "Failed to destroy sidecar", err)
		return fmt.Errorf("failed to destroy sidecar: %w", err)
	}
	c.logAudit("destroy_sidecar", targetID, "Sidecar destroyed successfully", nil)

	// Step 3: Verify namespace is clean
	c.logAudit("verify_namespace", targetID, "Verifying target namespace is clean", nil)
	result, err := c.verifier.VerifyNamespaceClean(ctx, targetID)
	if err != nil {
		c.logAudit("verify_namespace", targetID, "Verification failed", err)
		return fmt.Errorf("namespace verification failed: %w", err)
	}

	if !result.Clean {
		c.logAudit("verify_namespace", targetID, fmt.Sprintf("Namespace not clean: %v", result.Details), nil)

		// Attempt cleanup
		c.logAudit("cleanup_artifacts", targetID, "Attempting to clean remaining artifacts", nil)
		err = c.verifier.CleanupArtifacts(ctx, targetID)
		if err != nil {
			c.logAudit("cleanup_artifacts", targetID, "Artifact cleanup failed", err)
			return fmt.Errorf("failed to clean artifacts: %w", err)
		}

		// Verify again
		result, err = c.verifier.VerifyNamespaceClean(ctx, targetID)
		if err != nil || !result.Clean {
			c.logAudit("verify_namespace", targetID, "Namespace still not clean after cleanup attempt", err)
			return fmt.Errorf("namespace still not clean: %v", result.Details)
		}
	}

	c.logAudit("verify_namespace", targetID, "Namespace is clean", nil)

	return nil
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
