package sidecar

import (
	"fmt"
	"sync"
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

// TestManager_ConcurrentMapAccess_F14 guards against F-14. Emergency-stop
// watcher + main Execute cleanup both iterated ListSidecars and called
// DestroySidecar's map delete concurrently, tripping the Go runtime's
// "concurrent map writes" fatal. Exercise the same shape in-proc: many
// goroutines spamming Create / Destroy / List / Get / Exec paths that
// touch the map. Without mu this fails under `go test -race`.
func TestManager_ConcurrentMapAccess_F14(t *testing.T) {
	// Seed map with enough entries that ListSidecars / Destroy races fire.
	initial := make(map[string]string, 32)
	for i := 0; i < 32; i++ {
		initial[fmt.Sprintf("target-%d", i)] = fmt.Sprintf("sidecar-%d", i)
	}
	m := &Manager{createdSidecars: initial}

	var wg sync.WaitGroup

	// Delete-side: two "cleanup" goroutines both iterating the keyspace,
	// each taking the fast-path lookup-then-delete. Mirrors the F-14
	// emergency-stop-vs-Execute collision.
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 32; i++ {
				target := fmt.Sprintf("target-%d", i)
				// Mimic DestroySidecar's map ops without hitting docker.
				m.mu.RLock()
				_, exists := m.createdSidecars[target]
				m.mu.RUnlock()
				if !exists {
					continue
				}
				m.mu.Lock()
				delete(m.createdSidecars, target)
				m.mu.Unlock()
			}
		}()
	}

	// Insert-side: mirror CreateSidecar's register path for fresh keys.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100; i < 132; i++ {
			target := fmt.Sprintf("target-%d", i)
			sidecar := fmt.Sprintf("sidecar-%d", i)
			m.mu.Lock()
			m.createdSidecars[target] = sidecar
			m.mu.Unlock()
		}
	}()

	// Read-side: ListSidecars + GetSidecarID hammering while writers run.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				_ = m.ListSidecars()
				_, _ = m.GetSidecarID(fmt.Sprintf("target-%d", i%32))
			}
		}()
	}

	wg.Wait()
	// No assertions on final map state — goal is race-detector cleanliness.
	// If this test ever fails it will be a fatal from the Go runtime or a
	// WARNING from -race, not a t.Error.
}
