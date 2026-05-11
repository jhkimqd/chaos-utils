package disk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/blkiodev"
	"github.com/docker/docker/api/types/container"
)

// mockThrottleClient is a configurable fake for ThrottleDockerClient. Inspect
// returns the configured Resources; Update copies the patch into those
// Resources so the next Inspect reflects the daemon-side state, mimicking
// Docker's apply-then-readback behavior.
type mockThrottleClient struct {
	mu sync.Mutex

	current container.Resources

	// failUpdate causes ContainerUpdate to return an error.
	failUpdate error

	// swallowUpdate=true accepts but discards the patch, leaving current
	// unchanged. Models cgroup-v1's "accepted but didn't apply" quirk.
	swallowUpdate bool

	// inspectErr makes ContainerInspect fail (used to simulate post-update
	// verification skipping).
	inspectErr error

	// nthInspectErr injects an error on the Nth ContainerInspect call
	// (1-indexed). Used to verify behavior when the *re*-inspect fails.
	nthInspectErr  int
	inspectCallNum int

	updateCallCount int
}

func (m *mockThrottleClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inspectCallNum++
	if m.nthInspectErr > 0 && m.inspectCallNum == m.nthInspectErr {
		return types.ContainerJSON{}, fmt.Errorf("synthetic inspect failure on call %d", m.inspectCallNum)
	}
	if m.inspectErr != nil {
		return types.ContainerJSON{}, m.inspectErr
	}
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				Resources: cloneResources(m.current),
			},
		},
	}, nil
}

func (m *mockThrottleClient) ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCallCount++
	if m.failUpdate != nil {
		return container.ContainerUpdateOKBody{}, m.failUpdate
	}
	if !m.swallowUpdate {
		// Patch semantics: nil slice = preserve, any non-nil slice (incl.
		// empty) = replace. Matches our reading of Docker's update flow.
		if updateConfig.Resources.BlkioDeviceReadBps != nil {
			m.current.BlkioDeviceReadBps = updateConfig.Resources.BlkioDeviceReadBps
		}
		if updateConfig.Resources.BlkioDeviceWriteBps != nil {
			m.current.BlkioDeviceWriteBps = updateConfig.Resources.BlkioDeviceWriteBps
		}
		if updateConfig.Resources.BlkioDeviceReadIOps != nil {
			m.current.BlkioDeviceReadIOps = updateConfig.Resources.BlkioDeviceReadIOps
		}
		if updateConfig.Resources.BlkioDeviceWriteIOps != nil {
			m.current.BlkioDeviceWriteIOps = updateConfig.Resources.BlkioDeviceWriteIOps
		}
	}
	return container.ContainerUpdateOKBody{}, nil
}

func cloneResources(r container.Resources) container.Resources {
	r.BlkioDeviceReadBps = copyThrottleList(r.BlkioDeviceReadBps)
	r.BlkioDeviceWriteBps = copyThrottleList(r.BlkioDeviceWriteBps)
	r.BlkioDeviceReadIOps = copyThrottleList(r.BlkioDeviceReadIOps)
	r.BlkioDeviceWriteIOps = copyThrottleList(r.BlkioDeviceWriteIOps)
	return r
}

func stubResolver(device string) DeviceResolverFunc {
	return func(_ context.Context, _ ThrottleDockerClient, _, _ string) (string, error) {
		return device, nil
	}
}

func TestInjectThrottle_AllFourDimensions(t *testing.T) {
	mock := &mockThrottleClient{}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = stubResolver("/dev/sda")

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		TargetPath: "/var/lib/bor",
		ReadBps:    1024,
		WriteBps:   2048,
		ReadIOps:   10,
		WriteIOps:  20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !hasDevice(mock.current.BlkioDeviceReadBps, "/dev/sda") ||
		!hasDevice(mock.current.BlkioDeviceWriteBps, "/dev/sda") ||
		!hasDevice(mock.current.BlkioDeviceReadIOps, "/dev/sda") ||
		!hasDevice(mock.current.BlkioDeviceWriteIOps, "/dev/sda") {
		t.Fatalf("expected /dev/sda to appear in all four lists, got: %+v", mock.current)
	}
}

func TestInjectThrottle_OnlyWriteBpsLeavesOthersUntouched(t *testing.T) {
	// Pre-seed an unrelated cap on /dev/sdb so we can prove we don't touch it.
	mock := &mockThrottleClient{
		current: container.Resources{
			BlkioDeviceReadBps: []*blkiodev.ThrottleDevice{
				{Path: "/dev/sdb", Rate: 999},
			},
		},
	}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = stubResolver("/dev/sda")

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		TargetPath: "/var/lib/bor",
		WriteBps:   4096,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !hasDevice(mock.current.BlkioDeviceWriteBps, "/dev/sda") {
		t.Fatalf("expected /dev/sda in BlkioDeviceWriteBps, got: %+v", mock.current.BlkioDeviceWriteBps)
	}
	// ReadBps was pre-seeded with /dev/sdb and we didn't ask for read_bps,
	// so the list must be untouched (the inject path leaves it nil = no
	// change at the Docker SDK level; our mock preserves it).
	if !hasDevice(mock.current.BlkioDeviceReadBps, "/dev/sdb") {
		t.Fatalf("pre-existing /dev/sdb read cap was clobbered: %+v", mock.current.BlkioDeviceReadBps)
	}
	if hasDevice(mock.current.BlkioDeviceReadIOps, "/dev/sda") {
		t.Fatalf("ReadIOps shouldn't carry /dev/sda when only WriteBps was requested")
	}
}

func TestInjectThrottle_SnapshotsPreExistingAndRestoresOnRemove(t *testing.T) {
	// /dev/sda already has a 500 r-bps cap (operator-installed). After our
	// fault is torn down, that operator cap must remain.
	mock := &mockThrottleClient{
		current: container.Resources{
			BlkioDeviceReadBps: []*blkiodev.ThrottleDevice{
				{Path: "/dev/sda", Rate: 500},
			},
		},
	}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = stubResolver("/dev/sda")

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		TargetPath: "/var/lib/bor",
		WriteBps:   4096,
	})
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}

	if err := tw.RemoveFault(context.Background(), "abcdef123456789"); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	// Operator's read_bps cap must be intact.
	found := false
	for _, td := range mock.current.BlkioDeviceReadBps {
		if td.Path == "/dev/sda" && td.Rate == 500 {
			found = true
		}
	}
	if !found {
		t.Fatalf("pre-existing read_bps cap on /dev/sda was not restored: %+v", mock.current.BlkioDeviceReadBps)
	}
	// Our write_bps entry must be cleared.
	if hasDevice(mock.current.BlkioDeviceWriteBps, "/dev/sda") {
		t.Fatalf("write_bps cap on /dev/sda leaked after RemoveFault: %+v", mock.current.BlkioDeviceWriteBps)
	}
}

func TestInjectThrottle_PostUpdateVerificationFailsLoudly(t *testing.T) {
	// swallowUpdate models the cgroup-v1 case where ContainerUpdate accepts
	// the patch but the daemon couldn't apply it. The wrapper must detect
	// that and return an error rather than silently reporting success.
	mock := &mockThrottleClient{swallowUpdate: true}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = stubResolver("/dev/sda")

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		TargetPath: "/var/lib/bor",
		WriteBps:   4096,
	})
	if err == nil {
		t.Fatal("expected error when post-update verification finds no entry, got nil")
	}
	if !strings.Contains(err.Error(), "no BlkioDevice* entry for /dev/sda") {
		t.Errorf("expected verification error mentioning the device, got: %v", err)
	}
}

func TestInjectThrottle_DeviceResolverError(t *testing.T) {
	mock := &mockThrottleClient{}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = func(_ context.Context, _ ThrottleDockerClient, _, _ string) (string, error) {
		return "", errors.New("synthetic resolver failure")
	}

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		TargetPath: "/var/lib/bor",
		WriteBps:   4096,
	})
	if err == nil {
		t.Fatal("expected error from resolver failure")
	}
	if !strings.Contains(err.Error(), "synthetic resolver failure") {
		t.Errorf("expected resolver error to be wrapped, got: %v", err)
	}
}

func TestInjectThrottle_ExplicitDeviceSkipsResolver(t *testing.T) {
	mock := &mockThrottleClient{}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = func(_ context.Context, _ ThrottleDockerClient, _, _ string) (string, error) {
		t.Fatal("resolver must not be called when Device is set explicitly")
		return "", nil
	}

	err := tw.InjectThrottle(context.Background(), "abcdef123456789", ThrottleParams{
		Device:   "/dev/nvme0n1",
		WriteBps: 4096,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasDevice(mock.current.BlkioDeviceWriteBps, "/dev/nvme0n1") {
		t.Fatalf("expected /dev/nvme0n1 throttle entry, got: %+v", mock.current.BlkioDeviceWriteBps)
	}
}

func TestRemoveFault_NoSnapshotIsNoop(t *testing.T) {
	mock := &mockThrottleClient{}
	tw := NewThrottleWrapper(mock)

	if err := tw.RemoveFault(context.Background(), "never-injected"); err != nil {
		t.Fatalf("expected nil for un-snapshotted container, got %v", err)
	}
	if mock.updateCallCount != 0 {
		t.Errorf("RemoveFault on un-snapshotted container should not call Update, got %d calls", mock.updateCallCount)
	}
}

func TestConcurrentInject_DifferentContainers(t *testing.T) {
	mock := &mockThrottleClient{}
	tw := NewThrottleWrapper(mock)
	tw.DeviceResolver = stubResolver("/dev/sda")

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cid := fmt.Sprintf("container%012d", idx)
			errs[idx] = tw.InjectThrottle(context.Background(), cid, ThrottleParams{
				TargetPath: "/var/lib/bor",
				WriteBps:   1024,
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent inject %d failed: %v", i, err)
		}
	}

	tw.mu.Lock()
	got := len(tw.snapshots)
	tw.mu.Unlock()
	if got != 10 {
		t.Errorf("expected 10 snapshot entries, got %d", got)
	}
}

func TestValidateThrottleParams(t *testing.T) {
	tests := []struct {
		name    string
		params  ThrottleParams
		wantErr bool
	}{
		{"all four caps set, target path", ThrottleParams{TargetPath: "/data", ReadBps: 1, WriteBps: 1, ReadIOps: 1, WriteIOps: 1}, false},
		{"single cap, explicit device", ThrottleParams{Device: "/dev/sda", WriteBps: 1024}, false},
		{"missing both target_path and device", ThrottleParams{WriteBps: 1024}, true},
		{"all caps zero", ThrottleParams{TargetPath: "/data"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateThrottleParams(tc.params)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateThrottleParams() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestUpsertThrottle_PreservesOtherDevices(t *testing.T) {
	src := []*blkiodev.ThrottleDevice{
		{Path: "/dev/sdb", Rate: 100},
		{Path: "/dev/sda", Rate: 200},
		{Path: "/dev/sdc", Rate: 300},
	}
	got := upsertThrottle(src, "/dev/sda", 999)

	rateByPath := map[string]uint64{}
	for _, td := range got {
		rateByPath[td.Path] = td.Rate
	}
	if rateByPath["/dev/sda"] != 999 {
		t.Errorf("expected /dev/sda rate 999, got %d", rateByPath["/dev/sda"])
	}
	if rateByPath["/dev/sdb"] != 100 || rateByPath["/dev/sdc"] != 300 {
		t.Errorf("siblings clobbered: %+v", rateByPath)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 entries, got %d", len(got))
	}
}

func TestUpsertThrottle_AppendsWhenAbsent(t *testing.T) {
	src := []*blkiodev.ThrottleDevice{
		{Path: "/dev/sdb", Rate: 100},
	}
	got := upsertThrottle(src, "/dev/sda", 999)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if !hasDevice(got, "/dev/sda") {
		t.Errorf("expected /dev/sda in result, got: %+v", got)
	}
}
