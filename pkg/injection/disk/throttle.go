package disk

import (
	"context"
	"fmt"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/blkiodev"
	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog/log"
)

// ThrottleParams configures a blkio cgroup throttle on a container's backing
// host block device. Throttle persists until RemoveFault is called; there is
// no per-fault auto-expiry.
type ThrottleParams struct {
	// Device is the host whole-disk block device path (e.g. "/dev/sda").
	// When empty, ThrottleWrapper auto-resolves it from TargetPath.
	Device string

	// TargetPath is a path inside the container (e.g.
	// "/var/lib/bor/bor/chaindata") whose backing host device should be
	// throttled. Only used when Device is empty.
	TargetPath string

	// Per-dimension caps. Zero leaves a dimension untouched.
	ReadBps   uint64 // bytes/sec
	WriteBps  uint64 // bytes/sec
	ReadIOps  uint64 // ops/sec
	WriteIOps uint64 // ops/sec
}

// throttleSnapshot captures the four BlkioDevice* lists at first inject so
// RemoveFault can restore the pre-fault state byte-for-byte.
type throttleSnapshot struct {
	Device    string
	ReadBps   []*blkiodev.ThrottleDevice
	WriteBps  []*blkiodev.ThrottleDevice
	ReadIOps  []*blkiodev.ThrottleDevice
	WriteIOps []*blkiodev.ThrottleDevice
}

// ThrottleDockerClient is the subset of Docker API operations ThrottleWrapper
// uses. Mirrors stress.DockerClient so the same mock pattern works.
type ThrottleDockerClient interface {
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	ContainerUpdate(ctx context.Context, containerID string, updateConfig container.UpdateConfig) (container.ContainerUpdateOKBody, error)
}

// DeviceResolverFunc resolves a container path to a host whole-disk device.
// Exposed as a field on ThrottleWrapper so tests can stub it out without
// touching /proc/self/mountinfo.
type DeviceResolverFunc func(ctx context.Context, client ThrottleDockerClient, containerID, targetPath string) (string, error)

// ThrottleWrapper applies blkio cgroup throttles via Docker's ContainerUpdate.
type ThrottleWrapper struct {
	dockerClient ThrottleDockerClient

	// DeviceResolver maps (container, target_path) to a host whole-disk
	// device. Defaults to ResolveHostDevice; tests swap it for fakes.
	DeviceResolver DeviceResolverFunc

	mu        sync.Mutex
	snapshots map[string]throttleSnapshot
}

// NewThrottleWrapper creates a new disk-throttle wrapper.
func NewThrottleWrapper(dockerClient ThrottleDockerClient) *ThrottleWrapper {
	return &ThrottleWrapper{
		dockerClient:   dockerClient,
		DeviceResolver: ResolveHostDevice,
		snapshots:      make(map[string]throttleSnapshot),
	}
}

// InjectThrottle applies the requested blkio caps on the container's host
// block device. The first inject per container snapshots the pre-fault
// BlkioDevice* lists so RemoveFault can restore them. After ContainerUpdate
// we re-inspect and assert the new entry actually landed — ContainerUpdate
// silently accepts values it cannot apply on some cgroup v1 configurations
// (same lesson stress.injectCPULimit learned the hard way at line 196).
func (tw *ThrottleWrapper) InjectThrottle(ctx context.Context, containerID string, params ThrottleParams) error {
	inspect, err := tw.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	device := params.Device
	if device == "" {
		if tw.DeviceResolver == nil {
			return fmt.Errorf("disk_throttle: device is empty and no DeviceResolver configured")
		}
		resolved, derr := tw.DeviceResolver(ctx, tw.dockerClient, containerID, params.TargetPath)
		if derr != nil {
			return fmt.Errorf("disk_throttle: resolve host device from target_path %q: %w", params.TargetPath, derr)
		}
		device = resolved
	}

	tw.mu.Lock()
	if _, exists := tw.snapshots[containerID]; !exists {
		tw.snapshots[containerID] = throttleSnapshot{
			Device:    device,
			ReadBps:   copyThrottleList(inspect.HostConfig.Resources.BlkioDeviceReadBps),
			WriteBps:  copyThrottleList(inspect.HostConfig.Resources.BlkioDeviceWriteBps),
			ReadIOps:  copyThrottleList(inspect.HostConfig.Resources.BlkioDeviceReadIOps),
			WriteIOps: copyThrottleList(inspect.HostConfig.Resources.BlkioDeviceWriteIOps),
		}
	}
	tw.mu.Unlock()

	fmt.Printf("Injecting disk throttle on %s device=%s read_bps=%d write_bps=%d read_iops=%d write_iops=%d\n",
		containerID[:12], device, params.ReadBps, params.WriteBps, params.ReadIOps, params.WriteIOps)

	// Only touch the dimensions the user set. A nil slice in Resources is
	// interpreted as "no change" by Docker, so unset dimensions don't
	// disturb entries for other devices that the operator may have set up.
	res := container.Resources{}
	if params.ReadBps > 0 {
		res.BlkioDeviceReadBps = upsertThrottle(inspect.HostConfig.Resources.BlkioDeviceReadBps, device, params.ReadBps)
	}
	if params.WriteBps > 0 {
		res.BlkioDeviceWriteBps = upsertThrottle(inspect.HostConfig.Resources.BlkioDeviceWriteBps, device, params.WriteBps)
	}
	if params.ReadIOps > 0 {
		res.BlkioDeviceReadIOps = upsertThrottle(inspect.HostConfig.Resources.BlkioDeviceReadIOps, device, params.ReadIOps)
	}
	if params.WriteIOps > 0 {
		res.BlkioDeviceWriteIOps = upsertThrottle(inspect.HostConfig.Resources.BlkioDeviceWriteIOps, device, params.WriteIOps)
	}

	if _, err := tw.dockerClient.ContainerUpdate(ctx, containerID, container.UpdateConfig{Resources: res}); err != nil {
		return fmt.Errorf("failed to update container blkio throttle: %w", err)
	}

	post, perr := tw.dockerClient.ContainerInspect(ctx, containerID)
	if perr != nil {
		log.Warn().Err(perr).Str("container", containerID[:12]).Msg("could not re-inspect container to verify throttle landed; continuing")
		return nil
	}
	if !throttleApplied(post.HostConfig.Resources, device, params) {
		return fmt.Errorf("disk_throttle update accepted but no BlkioDevice* entry for %s was found post-update — the cap is not active (cgroup v1 quirk or unsupported kernel)", device)
	}

	fmt.Printf("Disk throttle active on %s (%s)\n", containerID[:12], device)
	return nil
}

// RemoveFault restores the four BlkioDevice* lists to their snapshotted state.
// If a list was empty pre-fault we send an explicit empty slice so Docker
// clears any entries we added rather than treating nil as "no change".
func (tw *ThrottleWrapper) RemoveFault(ctx context.Context, containerID string) error {
	tw.mu.Lock()
	snap, exists := tw.snapshots[containerID]
	tw.mu.Unlock()
	if !exists {
		// Throttle was never applied (or already removed); idempotent no-op.
		return nil
	}

	fmt.Printf("Removing disk throttle from %s (%s)\n", containerID[:12], snap.Device)

	res := container.Resources{
		BlkioDeviceReadBps:   ensureNotNil(snap.ReadBps),
		BlkioDeviceWriteBps:  ensureNotNil(snap.WriteBps),
		BlkioDeviceReadIOps:  ensureNotNil(snap.ReadIOps),
		BlkioDeviceWriteIOps: ensureNotNil(snap.WriteIOps),
	}

	if _, err := tw.dockerClient.ContainerUpdate(ctx, containerID, container.UpdateConfig{Resources: res}); err != nil {
		return fmt.Errorf("failed to restore container blkio throttle: %w", err)
	}

	post, perr := tw.dockerClient.ContainerInspect(ctx, containerID)
	if perr == nil {
		if leak := findThrottleLeak(post.HostConfig.Resources, snap); leak != "" {
			return fmt.Errorf("disk_throttle teardown verification failed: %s", leak)
		}
	}

	tw.mu.Lock()
	delete(tw.snapshots, containerID)
	tw.mu.Unlock()

	fmt.Printf("Disk throttle removed from %s\n", containerID[:12])
	return nil
}

// ValidateThrottleParams enforces the parameter contract: at least one
// dimension > 0, and a way to identify the target device.
func ValidateThrottleParams(params ThrottleParams) error {
	if params.TargetPath == "" && params.Device == "" {
		return fmt.Errorf("disk_throttle: one of target_path or device is required")
	}
	if params.ReadBps == 0 && params.WriteBps == 0 && params.ReadIOps == 0 && params.WriteIOps == 0 {
		return fmt.Errorf("disk_throttle: at least one of read_bps, write_bps, read_iops, write_iops must be > 0")
	}
	return nil
}

// copyThrottleList returns a deep-copied slice so the snapshot is immune to
// later mutation of the original Resources by the caller (or Docker).
func copyThrottleList(src []*blkiodev.ThrottleDevice) []*blkiodev.ThrottleDevice {
	if len(src) == 0 {
		return nil
	}
	dst := make([]*blkiodev.ThrottleDevice, 0, len(src))
	for _, td := range src {
		if td == nil {
			continue
		}
		c := *td
		dst = append(dst, &c)
	}
	return dst
}

// upsertThrottle returns a copy of src with the entry for `device` set to
// `rate`. Existing entries for OTHER devices are preserved verbatim, so a
// caller-set throttle on a sibling device isn't clobbered.
func upsertThrottle(src []*blkiodev.ThrottleDevice, device string, rate uint64) []*blkiodev.ThrottleDevice {
	out := make([]*blkiodev.ThrottleDevice, 0, len(src)+1)
	replaced := false
	for _, td := range src {
		if td == nil {
			continue
		}
		if td.Path == device {
			out = append(out, &blkiodev.ThrottleDevice{Path: device, Rate: rate})
			replaced = true
			continue
		}
		c := *td
		out = append(out, &c)
	}
	if !replaced {
		out = append(out, &blkiodev.ThrottleDevice{Path: device, Rate: rate})
	}
	return out
}

// ensureNotNil converts nil to an explicit empty slice so Docker treats the
// field as "clear all entries" rather than "leave unchanged".
func ensureNotNil(src []*blkiodev.ThrottleDevice) []*blkiodev.ThrottleDevice {
	if src == nil {
		return []*blkiodev.ThrottleDevice{}
	}
	return src
}

// throttleApplied reports whether `device` appears in at least one of the
// dimensions the user asked to throttle on. Used for post-update verification.
func throttleApplied(res container.Resources, device string, params ThrottleParams) bool {
	if params.ReadBps > 0 && hasDevice(res.BlkioDeviceReadBps, device) {
		return true
	}
	if params.WriteBps > 0 && hasDevice(res.BlkioDeviceWriteBps, device) {
		return true
	}
	if params.ReadIOps > 0 && hasDevice(res.BlkioDeviceReadIOps, device) {
		return true
	}
	if params.WriteIOps > 0 && hasDevice(res.BlkioDeviceWriteIOps, device) {
		return true
	}
	return false
}

func hasDevice(list []*blkiodev.ThrottleDevice, device string) bool {
	for _, td := range list {
		if td != nil && td.Path == device {
			return true
		}
	}
	return false
}

// findThrottleLeak returns a non-empty description if any entry for
// snap.Device remains at a rate that wasn't present in the saved snapshot.
// An empty string means the post-teardown state matches the snapshot.
func findThrottleLeak(res container.Resources, snap throttleSnapshot) string {
	dims := []struct {
		name string
		post []*blkiodev.ThrottleDevice
		orig []*blkiodev.ThrottleDevice
	}{
		{"read_bps", res.BlkioDeviceReadBps, snap.ReadBps},
		{"write_bps", res.BlkioDeviceWriteBps, snap.WriteBps},
		{"read_iops", res.BlkioDeviceReadIOps, snap.ReadIOps},
		{"write_iops", res.BlkioDeviceWriteIOps, snap.WriteIOps},
	}
	for _, d := range dims {
		for _, td := range d.post {
			if td == nil || td.Path != snap.Device {
				continue
			}
			var origRate uint64
			for _, otd := range d.orig {
				if otd != nil && otd.Path == snap.Device {
					origRate = otd.Rate
					break
				}
			}
			if td.Rate != origRate {
				return fmt.Sprintf("%s on %s still capped at %d post-teardown (snapshot expected %d)", d.name, snap.Device, td.Rate, origRate)
			}
		}
	}
	return ""
}
