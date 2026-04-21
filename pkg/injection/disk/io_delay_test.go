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

// isStartScript returns true for the InjectIODelay start script — it contains
// the pidfile setup tokens. The script also contains `dd ` and `seq`, so tests
// that want to capture the stress body can just inspect cmdStr directly.
func isStartScript(cmdStr string) bool {
	return strings.Contains(cmdStr, "PIDFILE=") && strings.Contains(cmdStr, "dd ")
}

// isVerifyCountScript returns true for the RemoveFault post-kill verify which
// counts chaos_io_stress processes still in /proc.
func isVerifyCountScript(cmdStr string) bool {
	return strings.Contains(cmdStr, "/proc/") && strings.Contains(cmdStr, "COUNT") && !strings.Contains(cmdStr, "PIDFILE=")
}

func TestInjectIODelay_DdFails(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("exec failed: no such container")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when start script exec fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to start I/O contention") {
		t.Errorf("expected 'failed to start I/O contention' error, got: %v", err)
	}
}

func TestInjectIODelay_AllWorkersDied(t *testing.T) {
	// Start script spawned 3 workers but all exited before the `kill -0` check.
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "0 3\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error when all workers died, got nil")
	}
	if !strings.Contains(err.Error(), "all 3 worker(s) exited") {
		t.Errorf("expected 'all N worker(s) exited' error, got: %v", err)
	}
}

func TestInjectIODelay_PidfileEmpty(t *testing.T) {
	// spawn block failed to write any PIDs (e.g., path not writable)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "0 0\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})
	if err == nil {
		t.Fatal("expected error when pidfile empty, got nil")
	}
	if !strings.Contains(err.Error(), "no workers spawned") {
		t.Errorf("expected 'no workers spawned' error, got: %v", err)
	}
}

func TestInjectIODelay_UnexpectedOutput(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "garbage output\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})
	if err == nil {
		t.Fatal("expected error when start script output is malformed, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected I/O contention start output") {
		t.Errorf("expected 'unexpected I/O contention start output' error, got: %v", err)
	}
}

func TestInjectIODelay_Success(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			// Single exec: the start script. Report 3 alive of 3 total.
			return "3 3\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callLog) != 1 {
		t.Fatalf("expected exactly 1 docker exec call (start+verify combined), got %d", len(callLog))
	}
	if !isStartScript(callLog[0]) {
		t.Errorf("expected start script (PIDFILE + dd), got: %s", callLog[0])
	}
	// Start script must detach worker stdio to avoid blocking docker exec.
	if !strings.Contains(callLog[0], "</dev/null >/dev/null 2>&1") {
		t.Errorf("worker subshells must redirect stdio to /dev/null, got: %s", callLog[0])
	}
	// Start script must verify via kill -0 on pidfile.
	if !strings.Contains(callLog[0], "kill -0") {
		t.Errorf("start script must verify workers via kill -0, got: %s", callLog[0])
	}
}

func TestInjectIODelay_PartialSurvival(t *testing.T) {
	// 2 of 3 workers alive — inject should succeed (partial is still chaos).
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "2 3\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})
	if err != nil {
		t.Fatalf("partial survival should not fail inject, got: %v", err)
	}
}

func TestRemoveFault_ReturnsNil(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if isVerifyCountScript(cmdStr) {
				// Post-kill verify — no processes remaining
				return "0", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err != nil {
		t.Fatalf("removal should succeed, got: %v", err)
	}
	if len(callLog) != 2 {
		t.Fatalf("expected exactly 2 docker exec calls (kill+rm combined, then verify), got %d", len(callLog))
	}
	// First call: combined kill-by-pid + marker sweep + cleanup
	if !strings.Contains(callLog[0], "kill") {
		t.Errorf("first command should contain kill, got: %s", callLog[0])
	}
	if !strings.Contains(callLog[0], "rm -f") {
		t.Errorf("first command should contain rm -f cleanup, got: %s", callLog[0])
	}
	if !strings.Contains(callLog[0], "PIDFILE=") {
		t.Errorf("first command should reference the pidfile, got: %s", callLog[0])
	}
	// Second call: post-kill verify
	if !isVerifyCountScript(callLog[1]) {
		t.Errorf("second command should be /proc verify, got: %s", callLog[1])
	}
}

func TestRemoveFault_ExecFails_ReturnsNil(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("exec failed")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
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
			if strings.Contains(cmdStr, "PIDFILE=") {
				// Kill script itself fails
				return "", fmt.Errorf("kill failed")
			}
			if isVerifyCountScript(cmdStr) {
				return "3", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
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
	// Even when the kill script returns success, if verify still sees
	// chaos_io_stress processes, we must surface the failure.
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if isVerifyCountScript(cmdStr) {
				return "2", nil
			}
			return "done", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
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

func TestValidateIODelayParams(t *testing.T) {
	tests := []struct {
		name    string
		params  IODelayParams
		wantErr bool
	}{
		{"valid all", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "all"}, false},
		{"valid read", IODelayParams{IOLatencyMs: 50, TargetPath: "/data", Operation: "read"}, false},
		{"valid write", IODelayParams{IOLatencyMs: 50, TargetPath: "/data", Operation: "write"}, false},
		{"negative latency", IODelayParams{IOLatencyMs: -1, TargetPath: "/data", Operation: "all"}, true},
		{"invalid operation", IODelayParams{IOLatencyMs: 100, TargetPath: "/data", Operation: "delete"}, true},
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

func TestWorkerCount(t *testing.T) {
	tests := []struct {
		name        string
		ioLatencyMs int
		want        int
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
			if got := workerCount(tt.ioLatencyMs); got != tt.want {
				t.Errorf("workerCount(%d) = %d, want %d", tt.ioLatencyMs, got, tt.want)
			}
		})
	}
}

func TestInjectIODelay_WorkerScaling(t *testing.T) {
	tests := []struct {
		name        string
		ioLatencyMs int
		wantWorkers int
	}{
		{"low latency 1 worker", 50, 1},
		{"medium 100 2 workers", 100, 2},
		{"high 200 3 workers", 200, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var startScript string
			mock := &mockDockerClientDisk{
				execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
					cmdStr := strings.Join(cmd, " ")
					if isStartScript(cmdStr) {
						startScript = cmdStr
					}
					return fmt.Sprintf("%d %d\n", tt.wantWorkers, tt.wantWorkers), nil
				},
			}

			iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
			err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
				TargetPath:  "/data",
				Operation:   "write",
				IOLatencyMs: tt.ioLatencyMs,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Script embeds "seq 1 N" for the loop bound.
			expected := fmt.Sprintf("seq 1 %d", tt.wantWorkers)
			if !strings.Contains(startScript, expected) {
				t.Errorf("expected %q in start script, got: %s", expected, startScript)
			}
		})
	}
}

func TestInjectIODelay_WriteOnly(t *testing.T) {
	var startScript string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if isStartScript(cmdStr) {
				startScript = cmdStr
			}
			return "1 1\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/data",
		Operation:   "write",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(startScript, "if=/dev/zero") {
		t.Error("write operation should read from /dev/zero")
	}
	if !strings.Contains(startScript, "conv=fdatasync") {
		t.Error("write operation should use fdatasync")
	}
	// Write-only must not create the read-side source file.
	if strings.Contains(startScript, "_src") {
		t.Error("write-only operation should not create a source file")
	}
}

func TestInjectIODelay_ReadOnly(t *testing.T) {
	var startScript string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if isStartScript(cmdStr) {
				startScript = cmdStr
			}
			return "1 1\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "/data",
		Operation:   "read",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(startScript, "_src") {
		t.Error("read operation should create a source file")
	}
	if !strings.Contains(startScript, "of=/dev/null") {
		t.Error("read operation should write to /dev/null")
	}
	// Read-only must not use fdatasync (that's write semantics).
	// Only the dd workers should be inspected — the seed `of=\"..._src\"` dd
	// doesn't use fdatasync either, so a simple Contains works.
	if strings.Contains(startScript, "fdatasync") {
		t.Error("read-only operation should not use fdatasync")
	}
}

func TestInjectIODelay_DefaultTargetPath(t *testing.T) {
	var startScript string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if isStartScript(cmdStr) {
				startScript = cmdStr
			}
			return "1 1\n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock, injectedPaths: map[string]string{}}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath:  "", // empty — should default to /tmp
		Operation:   "write",
		IOLatencyMs: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(startScript, "/tmp/.chaos_io_stress") {
		t.Errorf("empty TargetPath should default to /tmp, got: %s", startScript)
	}
	if !strings.Contains(startScript, "/tmp/.chaos_io_stress.pids") {
		t.Errorf("pidfile should live under /tmp, got: %s", startScript)
	}
}

func TestParseAliveTotal(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantAlive int
		wantTotal int
		wantErr   bool
	}{
		{"simple", "3 3", 3, 3, false},
		{"trailing newline", "2 3\n", 2, 3, false},
		{"zero zero", "0 0\n", 0, 0, false},
		{"surrounding blanks", "\n1 2\n\n", 1, 2, false},
		{"takes last line", "warning: blah\n1 2\n", 1, 2, false},
		{"empty", "", 0, 0, true},
		{"garbage", "hello world\n", 0, 0, true},
		{"one field", "3\n", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alive, total, err := parseAliveTotal(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAliveTotal err=%v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil {
				if alive != tt.wantAlive || total != tt.wantTotal {
					t.Errorf("parseAliveTotal = (%d,%d), want (%d,%d)", alive, total, tt.wantAlive, tt.wantTotal)
				}
			}
		})
	}
}
