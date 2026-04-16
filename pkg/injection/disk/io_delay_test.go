package disk

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type mockDockerClientDisk struct {
	execFunc func(ctx context.Context, containerID string, cmd []string) (string, error)
}

func (m *mockDockerClientDisk) ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error) {
	return m.execFunc(ctx, containerID, cmd)
}

func TestInjectIODelay_DdFails(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("exec failed: no such container")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when dd exec fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to start I/O contention") {
		t.Errorf("expected 'failed to start I/O contention' error, got: %v", err)
	}
}

func TestInjectIODelay_VerifyFails(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "/proc/") {
				// Verify command returns 0 dd processes
				return "0", nil
			}
			// dd stress command succeeds
			return "", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when no dd processes found, got nil")
	}
	if !strings.Contains(err.Error(), "no dd processes running") {
		t.Errorf("expected 'no dd processes running' error, got: %v", err)
	}
}

func TestInjectIODelay_Success(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if strings.Contains(cmdStr, "/proc/") {
				// Verify command returns 3 dd processes
				return "3", nil
			}
			// dd stress command succeeds
			return "", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callLog) < 2 {
		t.Errorf("expected at least 2 commands (dd stress + verify), got %d", len(callLog))
	}
	// First command should contain dd
	if !strings.Contains(callLog[0], "dd ") {
		t.Errorf("first command should be dd stress, got: %s", callLog[0])
	}
	// Second command should be /proc verification
	if !strings.Contains(callLog[1], "/proc/") {
		t.Errorf("second command should be /proc verification, got: %s", callLog[1])
	}
}

func TestRemoveFault_ReturnsNil(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				// Verify command — no processes remaining
				return "0", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err != nil {
		t.Fatalf("removal should succeed, got: %v", err)
	}
	if len(callLog) < 3 {
		t.Errorf("expected at least 3 commands (kill + verify + cleanup), got %d", len(callLog))
	}
	// First command should be the kill
	if !strings.Contains(callLog[0], "kill") {
		t.Errorf("first command should be kill, got: %s", callLog[0])
	}
	// Second command should be verify
	if !strings.Contains(callLog[1], "/proc/") {
		t.Errorf("second command should be /proc verification, got: %s", callLog[1])
	}
	// Third command should be rm cleanup
	if !strings.Contains(callLog[2], "rm -f") {
		t.Errorf("third command should be rm cleanup, got: %s", callLog[2])
	}
}

func TestRemoveFault_ExecFails_ReturnsNil(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("exec failed")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	// When all execs fail (including verify), we can't confirm processes are running,
	// so best-effort returns nil
	if err != nil {
		t.Fatalf("removal should succeed even if exec fails, got: %v", err)
	}
}

func TestRemoveFault_KillFails_ProcessesStillRunning(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "kill") {
				return "", fmt.Errorf("kill failed")
			}
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				// Verify shows processes still running
				return "3", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when kill fails and processes still running")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("expected 'still running' error, got: %v", err)
	}
}

func TestRemoveFault_KillSucceeds_ProcessesStillRunning(t *testing.T) {
	// Even when kill command returns success, if processes are still running, return error.
	// This catches the case where the kill script swallows individual kill errors via 2>/dev/null.
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				return "2", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when processes still running after kill")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("expected 'still running' error, got: %v", err)
	}
}

func TestRemoveFault_CleanupFails_ReturnsNil(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "rm -f") {
				return "", fmt.Errorf("cleanup failed")
			}
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				return "0", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	// Cleanup failure is non-critical — should still return nil
	if err != nil {
		t.Fatalf("removal should succeed even if cleanup fails, got: %v", err)
	}
}

func TestValidateIODelayParams(t *testing.T) {
	tests := []struct {
		name    string
		params  IODelayParams
		wantErr bool
	}{
		{"valid all", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all"}, false},
		{"valid read", IODelayParams{IOLatencyMs: 50, TargetPath: "/data", Operation: "read"}, false},
		{"valid write", IODelayParams{IOLatencyMs: 50, TargetPath: "/data", Operation: "write"}, false},
		{"valid method dd", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all", Method: "dd"}, false},
		{"valid method empty", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all", Method: ""}, false},
		{"negative latency", IODelayParams{IOLatencyMs: -1, TargetPath: "/data", Operation: "all"}, true},
		{"invalid operation", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "delete"}, true},
		{"valid method dm-delay", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all", Method: "dm-delay"}, false},
		{"invalid method", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all", Method: "ionice"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIODelayParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateIODelayParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateIODelayParams_DmDelayAccepted(t *testing.T) {
	err := ValidateIODelayParams(IODelayParams{
		IOLatencyMs: 100,
		TargetPath:  "/data",
		Operation:   "all",
		Method:      "dm-delay",
	})
	if err != nil {
		t.Fatalf("dm-delay method should be accepted, got error: %v", err)
	}
}

// --- Task 4: Comprehensive test coverage ---

func TestInjectIODelay_WorkerScaling(t *testing.T) {
	tests := []struct {
		name        string
		ioLatencyMs int
		wantWorkers int
	}{
		{"low latency 1 worker", 50, 1},
		{"boundary 99 1 worker", 99, 1},
		{"medium 100 2 workers", 100, 2},
		{"medium 199 2 workers", 199, 2},
		{"high 200 3 workers", 200, 3},
		{"very high 500 3 workers", 500, 3},
		{"zero 1 worker", 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stressCmd string
			mock := &mockDockerClientDisk{
				execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
					cmdStr := strings.Join(cmd, " ")
					if strings.Contains(cmdStr, "dd ") && strings.Contains(cmdStr, "seq") {
						stressCmd = cmdStr
						return "", nil
					}
					if strings.Contains(cmdStr, "/proc/") {
						return "1", nil
					}
					return "", nil
				},
			}

			iw := &IODelayWrapper{dockerClient: mock}
			err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
				TargetPath:  "/data",
				Operation:   "write",
				IOLatencyMs: tt.ioLatencyMs,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check that seq command uses correct worker count
			expected := fmt.Sprintf("seq 1 %d", tt.wantWorkers)
			if !strings.Contains(stressCmd, expected) {
				t.Errorf("expected %d workers (seq 1 %d), got command: %s", tt.wantWorkers, tt.wantWorkers, stressCmd)
			}
		})
	}
}

func TestInjectIODelay_WriteOnly(t *testing.T) {
	var stressCmd string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				return "1", nil
			}
			if strings.Contains(cmdStr, "dd ") {
				stressCmd = cmdStr
				return "", nil
			}
			return "", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/data",
		Operation:   "write",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Write-only should use dd if=/dev/zero of=... with fdatasync
	if !strings.Contains(stressCmd, "if=/dev/zero") {
		t.Error("write operation should read from /dev/zero")
	}
	if !strings.Contains(stressCmd, "conv=fdatasync") {
		t.Error("write operation should use fdatasync")
	}
	// Should NOT create a source file (that's for read operations)
	if strings.Contains(stressCmd, "_src") {
		t.Error("write-only operation should not create a source file")
	}
}

func TestInjectIODelay_ReadOnly(t *testing.T) {
	var stressCmd string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				return "1", nil
			}
			if strings.Contains(cmdStr, "dd ") {
				stressCmd = cmdStr
				return "", nil
			}
			return "", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/data",
		Operation:   "read",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read operation should first create a source file, then read from it
	if !strings.Contains(stressCmd, "_src") {
		t.Error("read operation should create a source file")
	}
	if !strings.Contains(stressCmd, "of=/dev/null") {
		t.Error("read operation should write to /dev/null")
	}
	// Should NOT use fdatasync (that's for write operations)
	if strings.Contains(stressCmd, "fdatasync") {
		t.Error("read-only operation should not use fdatasync")
	}
}

func TestInjectIODelay_DefaultTargetPath(t *testing.T) {
	var stressCmd string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") {
				return "1", nil
			}
			if strings.Contains(cmdStr, "dd ") {
				stressCmd = cmdStr
				return "", nil
			}
			return "", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "", // empty — should default to /tmp
		Operation:   "write",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(stressCmd, "/tmp/.chaos_io_stress") {
		t.Errorf("empty TargetPath should default to /tmp, got command: %s", stressCmd)
	}
}
