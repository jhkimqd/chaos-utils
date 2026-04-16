package disk

import (
	"context"
	"strings"
	"testing"
)

func TestFillRemoveFault_FallbackNoFindDelete(t *testing.T) {
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

	// The fallback should NOT use 'find / -delete' (dangerous system-wide search)
	if strings.Contains(capturedCmd, "find /") {
		t.Errorf("fallback should not use 'find / -delete', got: %s", capturedCmd)
	}
	if strings.Contains(capturedCmd, "-delete") {
		t.Errorf("fallback should not use '-delete', got: %s", capturedCmd)
	}
	// Should use targeted rm instead
	if !strings.Contains(capturedCmd, "rm -f") {
		t.Errorf("fallback should use 'rm -f' for targeted cleanup, got: %s", capturedCmd)
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
