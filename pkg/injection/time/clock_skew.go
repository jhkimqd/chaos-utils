package time

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ClockSkewParams defines parameters for clock skew injection
type ClockSkewParams struct {
	// Offset is the time offset (e.g., "+5m", "-1h", "+30s", "-2d")
	// Positive = future, Negative = past
	Offset string

	// DisableNTP blocks NTP traffic to prevent time correction
	DisableNTP bool
}

// ClockSkewWrapper wraps clock skew injection
type ClockSkewWrapper struct {
	dockerClient DockerClient
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// New creates a new clock skew wrapper
func New(dockerClient DockerClient) *ClockSkewWrapper {
	return &ClockSkewWrapper{
		dockerClient: dockerClient,
	}
}

// InjectClockSkew manipulates the system clock in a container.
// NOTE: Docker containers share the host kernel clock. This uses `date -s` which
// requires the SYS_TIME capability. In practice, this affects time for the
// target container's processes via the shared clock. Use with caution in
// multi-container environments.
func (cw *ClockSkewWrapper) InjectClockSkew(ctx context.Context, targetContainerID string, params ClockSkewParams) error {
	fmt.Printf("Injecting clock skew on target %s\n", targetContainerID[:12])

	// Save current epoch time for restoration
	saveCmd := []string{"sh", "-c", "date +%s > /tmp/.chaos_original_time"}
	_, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, saveCmd)
	if err != nil {
		fmt.Printf("  Warning: Could not save original time: %v\n", err)
	}

	// Block NTP if requested (prevents time correction)
	if params.DisableNTP {
		ntpCmd := []string{"sh", "-c", "iptables -A OUTPUT -p udp --dport 123 -j DROP -m comment --comment chaos-ntp-block 2>/dev/null || true"}
		_, _ = cw.dockerClient.ExecCommand(ctx, targetContainerID, ntpCmd)
		fmt.Printf("  NTP traffic blocked\n")
	}

	// Convert offset to seconds for POSIX-compatible date command
	// BusyBox date doesn't support GNU `date -d` — use epoch arithmetic instead
	offsetSec, err := parseOffsetToSeconds(params.Offset)
	if err != nil {
		return fmt.Errorf("invalid offset %q: %w", params.Offset, err)
	}

	// Apply clock offset: get current epoch, add offset, set new time
	// BusyBox `date -s` accepts "@epoch" format or "YYYY-MM-DD HH:MM:SS"
	// BusyBox date -s accepts "@epoch" format. No GNU fallback needed.
	cmd := []string{"sh", "-c", fmt.Sprintf(
		"NEW_EPOCH=$(( $(date +%%s) + %d )); date -s \"@$NEW_EPOCH\" 2>/dev/null || echo FAIL",
		offsetSec,
	)}

	output, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to skew clock (requires SYS_TIME capability): %w (output: %s)", err, output)
	}

	if strings.Contains(output, "FAIL") {
		return fmt.Errorf("failed to set system time — date command not supported or SYS_TIME capability missing")
	}

	// Verify the new time
	verifyCmd := []string{"date"}
	newTime, _ := cw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
	fmt.Printf("  Clock skewed by %s (%+ds) — new time: %s\n", params.Offset, offsetSec, strings.TrimSpace(newTime))

	return nil
}

// RemoveClockSkew restores the original clock
func (cw *ClockSkewWrapper) RemoveClockSkew(ctx context.Context, targetContainerID string, params ClockSkewParams) error {
	fmt.Printf("Restoring clock on target %s\n", targetContainerID[:12])

	// Restore NTP if it was blocked
	if params.DisableNTP {
		ntpCmd := []string{"sh", "-c", "iptables -D OUTPUT -p udp --dport 123 -j DROP -m comment --comment chaos-ntp-block 2>/dev/null || true"}
		_, _ = cw.dockerClient.ExecCommand(ctx, targetContainerID, ntpCmd)
		fmt.Printf("  NTP traffic restored\n")
	}

	// Restore from saved epoch time.
	// We saved the real epoch at injection time. Since the clock was skewed,
	// the current $(date +%s) includes the offset. We can't perfectly restore
	// (wall time has passed), but setting back to the original epoch is the
	// best we can do without NTP.
	restoreCmd := []string{"sh", "-c", `
		if [ -f /tmp/.chaos_original_time ]; then
			date -s "@$(cat /tmp/.chaos_original_time)" 2>/dev/null || true
			rm -f /tmp/.chaos_original_time
		fi
	`}

	_, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, restoreCmd)
	if err != nil {
		fmt.Printf("  Warning: Clock restoration may be imprecise: %v\n", err)
	}

	fmt.Printf("  Clock restored on target %s\n", targetContainerID[:12])
	return nil
}

// parseOffsetToSeconds converts offset strings like "+5m", "-1h", "+30s" to seconds
func parseOffsetToSeconds(offset string) (int, error) {
	if offset == "" {
		return 0, fmt.Errorf("offset must not be empty")
	}

	sign := 1
	rest := offset
	if strings.HasPrefix(offset, "+") {
		rest = offset[1:]
	} else if strings.HasPrefix(offset, "-") {
		sign = -1
		rest = offset[1:]
	}

	if len(rest) < 2 {
		return 0, fmt.Errorf("offset must have a numeric value and unit suffix (s/m/h/d)")
	}

	numStr := rest[:len(rest)-1]
	unit := rest[len(rest)-1:]

	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value %q in offset", numStr)
	}

	var multiplier int
	switch unit {
	case "s":
		multiplier = 1
	case "m":
		multiplier = 60
	case "h":
		multiplier = 3600
	case "d":
		multiplier = 86400
	default:
		return 0, fmt.Errorf("unknown unit %q (use s/m/h/d)", unit)
	}

	return sign * num * multiplier, nil
}

// ValidateClockSkewParams validates clock skew parameters
func ValidateClockSkewParams(params ClockSkewParams) error {
	if params.Offset == "" {
		return fmt.Errorf("offset must be specified (e.g., '+5m', '-1h', '+30s')")
	}

	_, err := parseOffsetToSeconds(params.Offset)
	if err != nil {
		return fmt.Errorf("invalid offset: %w", err)
	}

	return nil
}
