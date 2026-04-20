package disk

import (
	"context"
	"strings"
	"testing"
)

func TestFillRemoveFault_FallbackBoundedFind(t *testing.T) {
	var capturedCmd string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			capturedCmd = strings.Join(cmd, " ")
			return "done", nil
		},
	}

	fw := NewFillWrapper(mock)
	err := fw.RemoveFault(context.Background(), "abcdef123456789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The fallback must be bounded to common data roots, never system-wide.
	if strings.Contains(capturedCmd, "find / -") {
		t.Errorf("fallback must not traverse the root filesystem, got: %s", capturedCmd)
	}
	// Must restrict to specific roots and cap depth.
	for _, root := range []string{"/tmp", "/root", "/var/lib"} {
		if !strings.Contains(capturedCmd, root) {
			t.Errorf("fallback must search %s, got: %s", root, capturedCmd)
		}
	}
	if !strings.Contains(capturedCmd, "-maxdepth") {
		t.Errorf("fallback must cap traversal depth, got: %s", capturedCmd)
	}
	if !strings.Contains(capturedCmd, "chaos_fill_data") {
		t.Errorf("fallback must target chaos_fill_data files, got: %s", capturedCmd)
	}
}

func TestFillRemoveFault_TrackedPaths(t *testing.T) {
	var removedPaths []string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "rm -f") {
				removedPaths = append(removedPaths, cmd[len(cmd)-1])
			}
			return "", nil
		},
	}

	fw := NewFillWrapper(mock)
	// Simulate injected fills
	fw.mu.Lock()
	fw.injectedFills["abcdef123456789"] = []string{"/tmp/chaos_fill_data", "/var/lib/bor/chaos_fill_data"}
	fw.mu.Unlock()

	err := fw.RemoveFault(context.Background(), "abcdef123456789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removedPaths) != 2 {
		t.Errorf("expected 2 paths removed, got %d", len(removedPaths))
	}
}
