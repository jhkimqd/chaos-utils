package disk

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDmDelay_PreflightDmsetupNotFound(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "", fmt.Errorf("exec failed: dmsetup not found")
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error when dmsetup is not available")
	}
	if !strings.Contains(err.Error(), "dmsetup not available") {
		t.Errorf("expected 'dmsetup not available' error, got: %v", err)
	}
}

func TestDmDelay_PreflightNotPrivileged(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", fmt.Errorf("exit code 1")
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error when container is not privileged")
	}
	if !strings.Contains(err.Error(), "not privileged") {
		t.Errorf("expected 'not privileged' error, got: %v", err)
	}
}

func TestDmDelay_DeviceDiscovery(t *testing.T) {
	var discoveredDevice string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				// Capture which device was passed
				discoveredDevice = cmdStr
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/bor/chaindata",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(discoveredDevice, "/dev/sda1") {
		t.Errorf("expected blockdev to use discovered device /dev/sda1, got command: %s", discoveredDevice)
	}
}

func TestDmDelay_DeviceDiscovery_NotBlockDevice(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "tmpfs\n", nil // not a /dev/ device
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/tmp",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error for non-block-device path")
	}
	if !strings.Contains(err.Error(), "does not look like a block device") {
		t.Errorf("expected 'does not look like a block device' error, got: %v", err)
	}
}

func TestDmDelay_InjectSuccess(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				// Verify the table contains the correct delay value
				if !strings.Contains(cmdStr, "delay /dev/sda1 0 200") {
					t.Errorf("expected dmsetup create with 200ms delay, got: %s", cmdStr)
				}
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	containerID := "abcdef1234567890"
	err := dw.InjectDmDelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify mapping name contains container ID prefix
	if !dw.HasActiveMapping(containerID) {
		t.Error("expected active mapping after successful inject")
	}

	// Verify the dmsetup create command includes the short container ID
	found := false
	for _, cmd := range callLog {
		if strings.Contains(cmd, "dmsetup create chaos-delay-abcdef12") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected dmsetup create with mapping name containing container ID prefix")
	}
}

func TestDmDelay_InjectFails_BlockdevError(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "", fmt.Errorf("blockdev: cannot open /dev/sda1: Permission denied")
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error when blockdev fails")
	}
	if !strings.Contains(err.Error(), "failed to get device size") {
		t.Errorf("expected 'failed to get device size' error, got: %v", err)
	}
}

func TestDmDelay_InjectFails_DmsetupCreateError(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", fmt.Errorf("dmsetup: device already exists")
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err == nil {
		t.Fatal("expected error when dmsetup create fails")
	}
	if !strings.Contains(err.Error(), "failed to create dm-delay mapping") {
		t.Errorf("expected 'failed to create dm-delay mapping' error, got: %v", err)
	}
}

func TestDmDelay_InjectDuplicate(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	containerID := "abcdef1234567890"

	// First inject succeeds
	err := dw.InjectDmDelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})
	if err != nil {
		t.Fatalf("first inject failed: %v", err)
	}

	// Second inject on same container should fail
	err = dw.InjectDmDelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 300,
	})
	if err == nil {
		t.Fatal("expected error when injecting duplicate mapping")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Errorf("expected 'already active' error, got: %v", err)
	}
}

func TestDmDelay_RemoveSuccess(t *testing.T) {
	callLog := make([]string, 0)
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			callLog = append(callLog, cmdStr)
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup remove") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				// After removal, status returns "No such device"
				if len(callLog) > 6 { // after remove was called
					return "No such device", fmt.Errorf("exit 1")
				}
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	containerID := "abcdef1234567890"

	// Inject first
	err := dw.InjectDmDelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}

	if !dw.HasActiveMapping(containerID) {
		t.Fatal("expected active mapping after inject")
	}

	// Remove
	err = dw.RemoveDmDelay(context.Background(), containerID)
	if err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	if dw.HasActiveMapping(containerID) {
		t.Error("expected no active mapping after successful remove")
	}
}

func TestDmDelay_RemoveNoMapping(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			t.Error("no exec should be called when there's no active mapping")
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.RemoveDmDelay(context.Background(), "abcdef1234567890")
	if err != nil {
		t.Fatalf("remove with no mapping should succeed silently, got: %v", err)
	}
}

func TestDmDelay_RemoveFails_ReturnsError(t *testing.T) {
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup remove") {
				return "", fmt.Errorf("device busy")
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	containerID := "abcdef1234567890"

	// Inject first
	err := dw.InjectDmDelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
	})
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}

	// Remove should fail and return error (not silently swallow)
	err = dw.RemoveDmDelay(context.Background(), containerID)
	if err == nil {
		t.Fatal("expected error when dmsetup remove fails")
	}
	if !strings.Contains(err.Error(), "failed to remove dm-delay mapping") {
		t.Errorf("expected 'failed to remove dm-delay mapping' error, got: %v", err)
	}

	// Mapping should still be tracked since removal failed
	if !dw.HasActiveMapping(containerID) {
		t.Error("mapping should still be tracked after failed removal")
	}
}

func TestDmDelay_MappingName(t *testing.T) {
	tests := []struct {
		containerID string
		want        string
	}{
		{"abcdef1234567890", "chaos-delay-abcdef12"},
		{"12345678", "chaos-delay-12345678"},
		{"short", "chaos-delay-short"},
		{"abcdef12", "chaos-delay-abcdef12"},
	}

	for _, tt := range tests {
		got := mappingName(tt.containerID)
		if got != tt.want {
			t.Errorf("mappingName(%q) = %q, want %q", tt.containerID, got, tt.want)
		}
	}
}

func TestDmDelay_DefaultTargetPath(t *testing.T) {
	var discoveryCmd string
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				discoveryCmd = cmdStr
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	dw := NewDmDelayWrapper(mock)
	err := dw.InjectDmDelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "", // should default to /tmp
		Operation:   "all",
		IOLatencyMs: 200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(discoveryCmd, "/tmp") {
		t.Errorf("empty TargetPath should default to /tmp, got discovery cmd: %s", discoveryCmd)
	}
}

// Test that IODelayWrapper dispatches to dm-delay when method is set
func TestInjectIODelay_DmDelayMethodDispatch(t *testing.T) {
	var dmsetupCalled bool
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup") {
				dmsetupCalled = true
			}
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	iw := New(mock)
	err := iw.InjectIODelay(context.Background(), "abcdef1234567890", IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
		Method:      "dm-delay",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dmsetupCalled {
		t.Error("dm-delay method should dispatch to dmsetup, but no dmsetup commands were called")
	}
}

// Test that IODelayWrapper.RemoveFault handles dm-delay cleanup
func TestRemoveFault_DmDelayMethod(t *testing.T) {
	var dmsetupRemoveCalled bool
	mock := &mockDockerClientDisk{
		execFunc: func(ctx context.Context, containerID string, cmd []string) (string, error) {
			cmdStr := strings.Join(cmd, " ")
			if strings.Contains(cmdStr, "dmsetup version") {
				return "Library version: 1.02.175", nil
			}
			if strings.Contains(cmdStr, "test -d /dev/mapper") {
				return "", nil
			}
			if strings.Contains(cmdStr, "findmnt") || strings.Contains(cmdStr, "df") {
				return "/dev/sda1\n", nil
			}
			if strings.Contains(cmdStr, "blockdev --getsz") {
				return "2097152", nil
			}
			if strings.Contains(cmdStr, "dmsetup create") {
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup remove") {
				dmsetupRemoveCalled = true
				return "", nil
			}
			if strings.Contains(cmdStr, "dmsetup status") {
				if dmsetupRemoveCalled {
					return "No such device", fmt.Errorf("exit 1")
				}
				return "0 2097152 delay", nil
			}
			return "", nil
		},
	}

	iw := New(mock)
	containerID := "abcdef1234567890"

	// First inject with dm-delay method
	err := iw.InjectIODelay(context.Background(), containerID, IODelayParams{
		TargetPath:  "/var/lib/data",
		Operation:   "all",
		IOLatencyMs: 200,
		Method:      "dm-delay",
	})
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}

	// RemoveFault should detect dm-delay and clean it up
	err = iw.RemoveFault(context.Background(), containerID, IODelayParams{Operation: "all"})
	if err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if !dmsetupRemoveCalled {
		t.Error("RemoveFault should have called dmsetup remove for dm-delay mapping")
	}
}
