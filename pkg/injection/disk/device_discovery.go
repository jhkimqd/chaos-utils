package disk

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
)

// procMountinfoPath and sysBlockPath are package-level overrides so tests can
// substitute fixture data without touching the real /proc and /sys.
var (
	procMountinfoPath = "/proc/self/mountinfo"
	sysBlockPath      = "/sys/class/block"
)

// ResolveHostDevice maps a container path to the host whole-disk device whose
// I/O the kernel will throttle when we install a blkio entry for that device.
//
// Pipeline:
//  1. Inspect the container, find the bind-mount Source covering targetPath.
//     If no bind mount matches, fall back to GraphDriver.Data["UpperDir"]
//     (overlay2-backed paths).
//  2. Walk /proc/self/mountinfo to find the device that backs the resolved
//     host path (longest matching mount point wins).
//  3. If the device is a partition (e.g. /dev/sda1) walk up the sysfs tree
//     to the parent whole-disk device — blkio.throttle applies per
//     whole-disk, not per-partition.
func ResolveHostDevice(ctx context.Context, client ThrottleDockerClient, containerID, targetPath string) (string, error) {
	if targetPath == "" {
		return "", fmt.Errorf("target_path is empty; nothing to resolve")
	}

	inspect, err := client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect container: %w", err)
	}

	hostPath, err := containerPathToHostPath(inspect, targetPath)
	if err != nil {
		return "", err
	}

	device, err := findDeviceForPath(hostPath)
	if err != nil {
		return "", fmt.Errorf("resolve backing device for host path %q: %w", hostPath, err)
	}

	whole, err := wholeDiskFor(device)
	if err != nil {
		return "", fmt.Errorf("resolve whole-disk parent of %s: %w", device, err)
	}
	return whole, nil
}

// containerPathToHostPath finds the host filesystem path that backs targetPath
// inside the container. Bind mounts are preferred (Source is the host path);
// when nothing matches we fall back to the overlay2 UpperDir which is where
// container-writable layers live for non-bind paths.
func containerPathToHostPath(inspect types.ContainerJSON, targetPath string) (string, error) {
	cleanTarget := filepath.Clean(targetPath)

	bestLen := -1
	var bestMount *types.MountPoint
	for i := range inspect.Mounts {
		m := &inspect.Mounts[i]
		if m.Destination == "" || m.Source == "" {
			continue
		}
		cleanDest := filepath.Clean(m.Destination)
		if !pathUnder(cleanTarget, cleanDest) {
			continue
		}
		if len(cleanDest) > bestLen {
			bestLen = len(cleanDest)
			bestMount = m
		}
	}

	if bestMount != nil {
		rel := strings.TrimPrefix(cleanTarget, filepath.Clean(bestMount.Destination))
		rel = strings.TrimPrefix(rel, "/")
		return filepath.Join(bestMount.Source, rel), nil
	}

	// Fall back to overlay2 UpperDir — the writable layer for non-bind paths.
	// Anonymous volumes show up in inspect.Mounts already, so this branch is
	// reached only for paths inside the container's own overlay rootfs.
	if inspect.GraphDriver.Data != nil {
		if upper := inspect.GraphDriver.Data["UpperDir"]; upper != "" {
			return filepath.Join(upper, strings.TrimPrefix(cleanTarget, "/")), nil
		}
	}

	return "", fmt.Errorf("no bind mount covers %q and GraphDriver.UpperDir is unavailable — pass an explicit device: in the fault params", targetPath)
}

// pathUnder reports whether p is equal to or under base (both already cleaned).
func pathUnder(p, base string) bool {
	if base == "/" {
		return true
	}
	if p == base {
		return true
	}
	return strings.HasPrefix(p, base+"/")
}

// findDeviceForPath returns the device path that backs the filesystem
// containing hostPath, by walking /proc/self/mountinfo and selecting the
// entry whose mount point is the longest prefix of hostPath.
func findDeviceForPath(hostPath string) (string, error) {
	cleanPath := filepath.Clean(hostPath)

	f, err := os.Open(procMountinfoPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", procMountinfoPath, err)
	}
	defer f.Close()

	bestLen := -1
	var bestSource, bestFs, bestMp string

	scanner := bufio.NewScanner(f)
	// Some hosts have long mount-source paths from overlay2; raise the buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		// mountinfo:
		//   id parent maj:min root mp opts [tag1 ... tagN] - fs src super
		// Mandatory fields up through mp need 5 entries; the optional tag list
		// terminates at "-", and fs/src/super follow.
		if len(parts) < 10 {
			continue
		}
		dash := -1
		for i, p := range parts {
			if p == "-" {
				dash = i
				break
			}
		}
		if dash < 0 || dash+2 >= len(parts) {
			continue
		}
		mp := unescapeMountinfo(parts[4])
		fs := parts[dash+1]
		src := unescapeMountinfo(parts[dash+2])
		cleanMp := filepath.Clean(mp)
		if !pathUnder(cleanPath, cleanMp) {
			continue
		}
		if len(cleanMp) > bestLen {
			bestLen = len(cleanMp)
			bestSource = src
			bestFs = fs
			bestMp = cleanMp
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan %s: %w", procMountinfoPath, err)
	}

	if bestLen < 0 {
		return "", fmt.Errorf("no mountinfo entry covers host path %q", hostPath)
	}

	if !strings.HasPrefix(bestSource, "/dev/") {
		return "", fmt.Errorf("mount point %s is backed by %q (fs=%s), not a /dev block device — disk_throttle has no effect on tmpfs/nfs/overlay; pass an explicit device: param", bestMp, bestSource, bestFs)
	}
	return bestSource, nil
}

// wholeDiskFor returns the parent whole-disk device for a partition device.
// blkio.throttle applies per whole-disk on cgroup v1 (sda, not sda1); cgroup
// v2 io.max accepts partitions but the abstraction in Docker's SDK passes the
// same lists either way, so we normalise to the whole-disk form.
func wholeDiskFor(device string) (string, error) {
	basename := filepath.Base(device)
	partFile := filepath.Join(sysBlockPath, basename, "partition")
	if _, err := os.Stat(partFile); err != nil {
		// Not a partition (or sysfs unavailable). Caller-supplied or
		// already-whole-disk devices land here.
		return device, nil
	}
	// It's a partition. The sysfs entry is a symlink whose parent directory
	// is the whole-disk device:
	//   /sys/class/block/sda1 -> ../../devices/.../sda/sda1
	//   parent dir name = "sda"
	real, err := filepath.EvalSymlinks(filepath.Join(sysBlockPath, basename))
	if err != nil {
		return "", fmt.Errorf("eval %s: %w", filepath.Join(sysBlockPath, basename), err)
	}
	parent := filepath.Base(filepath.Dir(real))
	if parent == "" || parent == "." || parent == "block" {
		// Already at the whole-disk root in sysfs; treat input as canonical.
		return device, nil
	}
	return "/dev/" + parent, nil
}

// unescapeMountinfo decodes the octal escapes mountinfo uses for whitespace
// and backslash in path fields (space \040, tab \011, newline \012, \\ \134).
func unescapeMountinfo(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if c, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(c))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
