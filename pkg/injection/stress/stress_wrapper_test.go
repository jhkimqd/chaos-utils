package stress

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

type mockDockerClientStress struct {
	execFunc      func(ctx context.Context, containerID string, cmd []string) (string, error)
	inspectReturn types.ContainerJSON
	inspectErr    error
	updateReturn  container.ContainerUpdateOKBody
	updateErr     error
}

func (m *mockDockerClientStress) ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error) {
	return m.execFunc(ctx, containerID, cmd)
}

func (m *mockDockerClientStress) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	return m.inspectReturn, m.inspectErr
}

func (m *mockDockerClientStress) ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error) {
	return m.updateReturn, m.updateErr
}

func TestInjectActiveCPUStress_VerifiesProcesses(t *testing.T) {
	mock := &mockDockerClientStress{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "pgrep") {
				return "4", nil // 4 yes processes running
			}
			// stress injection command
			return "", nil
		},
	}

	sw := &StressWrapper{
		dockerClient:      mock,
		originalResources: make(map[string]container.Resources),
	}

	err := sw.InjectCPUStress(context.Background(), "abcdef123456789", StressParams{
		Method:     "stress",
		CPUPercent: 80,
		Cores:      4,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInjectActiveCPUStress_FailsWhenNoProcesses(t *testing.T) {
	mock := &mockDockerClientStress{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "pgrep") {
				return "", fmt.Errorf("no processes found")
			}
			return "", nil
		},
	}

	sw := &StressWrapper{
		dockerClient:      mock,
		originalResources: make(map[string]container.Resources),
	}

	err := sw.InjectCPUStress(context.Background(), "abcdef123456789", StressParams{
		Method:     "stress",
		CPUPercent: 80,
		Cores:      2,
	})

	if err == nil {
		t.Fatal("expected error when stress processes not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected 'not running' error, got: %v", err)
	}
}

func TestInjectActiveCPUStress_FailsWhenZeroCount(t *testing.T) {
	mock := &mockDockerClientStress{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "pgrep") {
				return "0", nil
			}
			return "", nil
		},
	}

	sw := &StressWrapper{
		dockerClient:      mock,
		originalResources: make(map[string]container.Resources),
	}

	err := sw.InjectCPUStress(context.Background(), "abcdef123456789", StressParams{
		Method:     "stress",
		CPUPercent: 80,
		Cores:      2,
	})

	if err == nil {
		t.Fatal("expected error when pgrep returns 0")
	}
	if !strings.Contains(err.Error(), "expected 2") {
		t.Errorf("expected error about expected process count, got: %v", err)
	}
}

func TestInjectCPULimit_Success(t *testing.T) {
	mock := &mockDockerClientStress{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			return "", nil
		},
		inspectReturn: types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				HostConfig: &container.HostConfig{
					Resources: container.Resources{
						CPUQuota:  100000,
						CPUPeriod: 100000,
					},
				},
			},
		},
		updateReturn: container.ContainerUpdateOKBody{},
	}

	sw := &StressWrapper{
		dockerClient:      mock,
		originalResources: make(map[string]container.Resources),
	}

	err := sw.InjectCPUStress(context.Background(), "abcdef123456789", StressParams{
		Method:     "limit",
		CPUPercent: 50,
		Cores:      1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify original resources were saved
	if _, exists := sw.originalResources["abcdef123456789"]; !exists {
		t.Error("original resources should be saved")
	}
}

func TestValidateStressParams(t *testing.T) {
	tests := []struct {
		name    string
		params  StressParams
		wantErr bool
	}{
		{"valid", StressParams{CPUPercent: 50, MemoryMB: 512, Cores: 2}, false},
		{"zero values", StressParams{}, false},
		{"cpu too high", StressParams{CPUPercent: 101}, true},
		{"cpu negative", StressParams{CPUPercent: -1}, true},
		{"memory negative", StressParams{MemoryMB: -1}, true},
		{"cores negative", StressParams{Cores: -1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStressParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateStressParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
