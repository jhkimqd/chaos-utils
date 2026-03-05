package sidecar

import (
	"testing"
)

func TestCreateSidecar_Idempotent(t *testing.T) {
	// Test that calling CreateSidecar twice for the same target
	// returns the existing sidecar ID without error
	m := &Manager{
		sidecarImage:    "test-image:latest",
		createdSidecars: make(map[string]string),
	}

	// Pre-populate a sidecar entry
	targetID := "target-container-id-123456"
	existingSidecarID := "sidecar-container-id-789012"
	m.createdSidecars[targetID] = existingSidecarID

	// Second create should reuse
	sidecarID, err := m.CreateSidecar(nil, targetID)
	if err != nil {
		t.Fatalf("expected no error on duplicate CreateSidecar, got: %v", err)
	}
	if sidecarID != existingSidecarID {
		t.Errorf("expected reuse of sidecar %s, got %s", existingSidecarID, sidecarID)
	}
}

func TestDestroySidecar_Idempotent(t *testing.T) {
	// Test that destroying a non-existent sidecar returns nil
	m := &Manager{
		createdSidecars: make(map[string]string),
	}

	err := m.DestroySidecar(nil, "nonexistent-target")
	if err != nil {
		t.Fatalf("expected nil error for non-existent sidecar, got: %v", err)
	}
}

func TestListSidecars_ReturnsCopy(t *testing.T) {
	m := &Manager{
		createdSidecars: map[string]string{
			"target1": "sidecar1",
			"target2": "sidecar2",
		},
	}

	list := m.ListSidecars()
	if len(list) != 2 {
		t.Errorf("expected 2 sidecars, got %d", len(list))
	}

	// Mutating the copy should not affect the original
	list["target3"] = "sidecar3"
	if len(m.createdSidecars) != 2 {
		t.Error("modifying returned map should not affect internal state")
	}
}

func TestGetSidecarID(t *testing.T) {
	m := &Manager{
		createdSidecars: map[string]string{
			"target1": "sidecar1",
		},
	}

	id, exists := m.GetSidecarID("target1")
	if !exists || id != "sidecar1" {
		t.Error("expected to find existing sidecar")
	}

	_, exists = m.GetSidecarID("nonexistent")
	if exists {
		t.Error("expected not to find nonexistent sidecar")
	}
}
