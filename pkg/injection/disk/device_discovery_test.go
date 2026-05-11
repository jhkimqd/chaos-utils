package disk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
)

// writeMountinfo writes a synthetic /proc/self/mountinfo to a temp file and
// returns the path. The format mirrors what the kernel emits: space-separated
// fields with an in-line "-" separator before the fs/source/super fields.
func writeMountinfo(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mountinfo-*")
	if err != nil {
		t.Fatalf("create mountinfo: %v", err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatalf("write mountinfo: %v", err)
		}
	}
	return f.Name()
}

// withMountinfo points procMountinfoPath at a fixture for the duration of t.
func withMountinfo(t *testing.T, path string) {
	t.Helper()
	orig := procMountinfoPath
	procMountinfoPath = path
	t.Cleanup(func() { procMountinfoPath = orig })
}

// withSysBlock points sysBlockPath at a synthesised sysfs layout for t.
// `partitions` maps "<base>" -> "<parent base>" for entries that should look
// like partitions. Entries not in the map are treated as whole disks.
func withSysBlock(t *testing.T, partitions map[string]string) {
	t.Helper()
	root := t.TempDir()
	// Build a fake hierarchy:
	//   <root>/_devices/<parent>/<part>   (real directories)
	//   <root>/sys/class/block/<name> -> symlink into _devices
	devicesRoot := filepath.Join(root, "_devices")
	classBlock := filepath.Join(root, "sys", "class", "block")
	if err := os.MkdirAll(classBlock, 0o755); err != nil {
		t.Fatalf("mkdir class/block: %v", err)
	}
	// Materialise parents first so partition dirs can sit underneath them.
	parents := map[string]struct{}{}
	for _, parent := range partitions {
		parents[parent] = struct{}{}
	}
	for parent := range parents {
		if err := os.MkdirAll(filepath.Join(devicesRoot, parent), 0o755); err != nil {
			t.Fatalf("mkdir parent %s: %v", parent, err)
		}
		// Whole-disk sysfs symlink: /sys/class/block/<parent> -> _devices/<parent>
		if err := os.Symlink(filepath.Join(devicesRoot, parent), filepath.Join(classBlock, parent)); err != nil {
			t.Fatalf("symlink parent %s: %v", parent, err)
		}
	}
	for part, parent := range partitions {
		partDir := filepath.Join(devicesRoot, parent, part)
		if err := os.MkdirAll(partDir, 0o755); err != nil {
			t.Fatalf("mkdir partition %s: %v", part, err)
		}
		// Mark as partition by creating the `partition` file with the
		// partition number — sysfs convention; ResolveHostDevice only
		// checks existence.
		if err := os.WriteFile(filepath.Join(partDir, "partition"), []byte("1\n"), 0o644); err != nil {
			t.Fatalf("write partition file %s: %v", part, err)
		}
		if err := os.Symlink(partDir, filepath.Join(classBlock, part)); err != nil {
			t.Fatalf("symlink partition %s: %v", part, err)
		}
	}
	orig := sysBlockPath
	sysBlockPath = classBlock
	t.Cleanup(func() { sysBlockPath = orig })
}

func TestFindDeviceForPath_LongestPrefixWins(t *testing.T) {
	withMountinfo(t, writeMountinfo(t, []string{
		// id par maj:min root mp     opts shared - fs   src      super
		"40 0 8:0 / / rw - ext4 /dev/sda1 rw",
		"45 40 8:16 / /var/lib/docker rw - ext4 /dev/sdb1 rw",
		"50 45 8:16 / /var/lib/docker/volumes/foo rw - ext4 /dev/sdb1 rw",
	}))

	got, err := findDeviceForPath("/var/lib/docker/volumes/foo/_data/sub")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sdb1" {
		t.Errorf("expected /dev/sdb1 (longest matching mount), got %s", got)
	}
}

func TestFindDeviceForPath_NonBlockDeviceFails(t *testing.T) {
	withMountinfo(t, writeMountinfo(t, []string{
		"40 0 0:1 / / rw - tmpfs tmpfs rw",
	}))

	_, err := findDeviceForPath("/some/path")
	if err == nil {
		t.Fatal("expected error for tmpfs-backed path, got nil")
	}
	if !strings.Contains(err.Error(), "tmpfs") {
		t.Errorf("expected error to mention the fs type, got: %v", err)
	}
}

func TestFindDeviceForPath_NoMatch(t *testing.T) {
	withMountinfo(t, writeMountinfo(t, []string{
		"40 0 8:0 / /opt/elsewhere rw - ext4 /dev/sda1 rw",
	}))

	_, err := findDeviceForPath("/var/lib/bor/chaindata")
	if err == nil {
		t.Fatal("expected error when no mount covers the path")
	}
	if !strings.Contains(err.Error(), "no mountinfo entry") {
		t.Errorf("expected 'no mountinfo entry' error, got: %v", err)
	}
}

func TestWholeDiskFor_PartitionResolvesToParent(t *testing.T) {
	withSysBlock(t, map[string]string{
		"sda1": "sda",
	})
	got, err := wholeDiskFor("/dev/sda1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sda" {
		t.Errorf("expected /dev/sda, got %s", got)
	}
}

func TestWholeDiskFor_NVMePartitionResolvesToParent(t *testing.T) {
	withSysBlock(t, map[string]string{
		"nvme0n1p1": "nvme0n1",
	})
	got, err := wholeDiskFor("/dev/nvme0n1p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/nvme0n1" {
		t.Errorf("expected /dev/nvme0n1, got %s", got)
	}
}

func TestWholeDiskFor_NotAPartitionPassesThrough(t *testing.T) {
	// Empty sysfs fixture: no `partition` file exists, so the Stat fails and
	// wholeDiskFor returns the input unchanged.
	withSysBlock(t, nil)
	got, err := wholeDiskFor("/dev/sda")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/dev/sda" {
		t.Errorf("expected /dev/sda passthrough, got %s", got)
	}
}

func TestResolveHostDevice_BindMountToExt4Partition(t *testing.T) {
	withMountinfo(t, writeMountinfo(t, []string{
		"40 0 8:0 / / rw - ext4 /dev/sda1 rw",
		"42 40 8:0 / /mnt/host-volumes rw - ext4 /dev/sda1 rw",
	}))
	withSysBlock(t, map[string]string{
		"sda1": "sda",
	})

	mock := &mockThrottleClient{}
	inspect := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{},
		Mounts: []types.MountPoint{{
			Type:        "bind",
			Source:      "/mnt/host-volumes/bor-data",
			Destination: "/var/lib/bor",
		}},
	}
	// We bypass the normal mock and call helpers directly so the test
	// exercises ResolveHostDevice's three-stage pipeline end-to-end without
	// reaching into ContainerInspect-mocking machinery.
	host, err := containerPathToHostPath(inspect, "/var/lib/bor/bor/chaindata")
	if err != nil {
		t.Fatalf("containerPathToHostPath: %v", err)
	}
	if host != "/mnt/host-volumes/bor-data/bor/chaindata" {
		t.Errorf("unexpected host path: %s", host)
	}
	device, err := findDeviceForPath(host)
	if err != nil {
		t.Fatalf("findDeviceForPath: %v", err)
	}
	whole, err := wholeDiskFor(device)
	if err != nil {
		t.Fatalf("wholeDiskFor: %v", err)
	}
	if whole != "/dev/sda" {
		t.Errorf("expected /dev/sda, got %s", whole)
	}
	_ = mock
}

func TestContainerPathToHostPath_LongestMountWins(t *testing.T) {
	inspect := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{},
		Mounts: []types.MountPoint{
			{Type: "bind", Source: "/host-broad", Destination: "/"},
			{Type: "bind", Source: "/host-narrow", Destination: "/var/lib/bor"},
			{Type: "bind", Source: "/host-other", Destination: "/etc/heimdall"},
		},
	}
	got, err := containerPathToHostPath(inspect, "/var/lib/bor/bor/chaindata")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/host-narrow/bor/chaindata"
	if got != want {
		t.Errorf("expected %s (most specific mount), got %s", want, got)
	}
}

func TestContainerPathToHostPath_OverlayFallback(t *testing.T) {
	inspect := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			GraphDriver: types.GraphDriverData{
				Name: "overlay2",
				Data: map[string]string{"UpperDir": "/var/lib/docker/overlay2/abc/diff"},
			},
		},
		Mounts: nil,
	}
	got, err := containerPathToHostPath(inspect, "/var/lib/bor/chaindata")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/var/lib/docker/overlay2/abc/diff/var/lib/bor/chaindata"
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestContainerPathToHostPath_NoMountNoOverlayFails(t *testing.T) {
	inspect := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{},
		Mounts:            nil,
	}
	_, err := containerPathToHostPath(inspect, "/var/lib/bor/chaindata")
	if err == nil {
		t.Fatal("expected error when neither bind mount nor overlay metadata exist")
	}
	if !strings.Contains(err.Error(), "explicit device") {
		t.Errorf("expected helpful hint mentioning explicit device, got: %v", err)
	}
}

func TestUnescapeMountinfo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/var/lib/bor", "/var/lib/bor"},
		{`/var/lib/with\040space`, "/var/lib/with space"},
		{`a\040b\040c`, "a b c"},
		{`/tab\011here`, "/tab\there"},
		// Trailing backslash: leave it alone, not enough bytes for an escape.
		{`/trailing\`, `/trailing\`},
	}
	for _, tc := range tests {
		got := unescapeMountinfo(tc.in)
		if got != tc.want {
			t.Errorf("unescapeMountinfo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveHostDevice_EmptyTargetPathFails(t *testing.T) {
	_, err := ResolveHostDevice(context.Background(), &mockThrottleClient{}, "abcdef", "")
	if err == nil {
		t.Fatal("expected error for empty target_path")
	}
	if !strings.Contains(err.Error(), "target_path is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveHostDevice_InspectErrorWrapped(t *testing.T) {
	mock := &mockThrottleClient{inspectErr: errors.New("synthetic inspect failure")}
	_, err := ResolveHostDevice(context.Background(), mock, "abcdef", "/var/lib/bor")
	if err == nil {
		t.Fatal("expected error when ContainerInspect fails")
	}
	if !strings.Contains(err.Error(), "synthetic inspect failure") {
		t.Errorf("expected wrapped inspect error, got: %v", err)
	}
}
