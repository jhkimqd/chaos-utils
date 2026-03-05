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

func TestInjectIODelay_LsofFails(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("lsof: command not found")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when lsof fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to find processes") {
		t.Errorf("expected 'failed to find processes' error, got: %v", err)
	}
}

func TestInjectIODelay_EmptyPIDOutput(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			// lsof succeeds but returns no PIDs
			return "  \n", nil
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err == nil {
		t.Fatal("expected error when no processes found, got nil")
	}
	if !strings.Contains(err.Error(), "no processes found") {
		t.Errorf("expected 'no processes found' error, got: %v", err)
	}
}

func TestInjectIODelay_Success(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if strings.Contains(cmdStr, "lsof") {
				return "1234", nil
			}
			if strings.Contains(cmdStr, "ionice") {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s", cmdStr)
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.InjectIODelay(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(callLog) < 2 {
		t.Errorf("expected at least 2 commands (lsof + ionice), got %d", len(callLog))
	}
}

func TestRemoveFault_LsofFails_ReturnsNil(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", fmt.Errorf("lsof: command not found")
		},
	}

	iw := &IODelayWrapper{dockerClient: mock}
	err := iw.RemoveFault(context.Background(), "abcdef123456789", IODelayParams{
		TargetPath: "/var/lib/data",
		Operation:  "all",
	})

	// During removal, best-effort is fine
	if err != nil {
		t.Fatalf("removal should succeed even if lsof fails, got: %v", err)
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
