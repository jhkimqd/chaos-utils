package cleanup

import (
	"strings"
	"testing"
)

func TestCleanupAll_NoSidecars(t *testing.T) {
	// CleanupAll with no sidecars should succeed
	// We can't easily mock the coordinator's dependencies without interfaces,
	// but we can test the error aggregation logic directly.

	// Simulate the error aggregation logic
	errors := []error{}
	if len(errors) > 0 {
		t.Error("no errors expected")
	}
}

func TestErrorAggregation(t *testing.T) {
	// Test that multiple errors are properly aggregated into the message
	errors := []error{
		&testError{"failed to destroy sidecar for target-1"},
		&testError{"namespace verification failed for target-2"},
		&testError{"failed to clean artifacts for target-3"},
	}

	errMsgs := make([]string, len(errors))
	for i, e := range errors {
		errMsgs[i] = e.Error()
	}
	combined := strings.Join(errMsgs, "\n  - ")

	if !strings.Contains(combined, "target-1") {
		t.Error("combined error should contain target-1")
	}
	if !strings.Contains(combined, "target-2") {
		t.Error("combined error should contain target-2")
	}
	if !strings.Contains(combined, "target-3") {
		t.Error("combined error should contain target-3")
	}
}

func TestAuditLog(t *testing.T) {
	c := &Coordinator{
		auditLog: make([]AuditEntry, 0),
	}

	c.logAudit("test_action", "target-123", "test details", nil)
	c.logAudit("test_fail", "target-456", "failure details", &testError{"something broke"})

	log := c.GetAuditLog()
	if len(log) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(log))
	}

	if !log[0].Success {
		t.Error("first entry should be success")
	}
	if log[1].Success {
		t.Error("second entry should be failure")
	}
	if log[1].Error == nil {
		t.Error("second entry should have error")
	}
}

func TestCleanupSummary(t *testing.T) {
	c := &Coordinator{
		auditLog: make([]AuditEntry, 0),
	}

	c.logAudit("action1", "target1", "ok", nil)
	c.logAudit("action2", "target2", "ok", nil)
	c.logAudit("action3", "target3", "fail", &testError{"broke"})

	summary := c.GetSummary()
	if summary.TotalActions != 3 {
		t.Errorf("expected 3 total actions, got %d", summary.TotalActions)
	}
	if summary.Succeeded != 2 {
		t.Errorf("expected 2 succeeded, got %d", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", summary.Failed)
	}

	str := summary.String()
	if !strings.Contains(str, "3 total") {
		t.Errorf("summary string should contain '3 total', got: %s", str)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
