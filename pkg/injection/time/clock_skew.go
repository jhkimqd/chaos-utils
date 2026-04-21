// Package time implements clock-skew fault injection.
//
// CAVEAT: the mechanism (`date -s` + optional iptables DROP on UDP/123) runs
// inside the target container but mutates the *host* kernel clock, because
// Docker containers share the host's clock namespace. This means running
// clock_skew on any target shifts time for every container on the same host.
// Use only in isolated dev environments. Injection is refused unless the
// CHAOS_ALLOW_HOST_CLOCK_SKEW=1 env var is set by the operator.
package time

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// ClockSkewParams defines parameters for clock skew injection.
type ClockSkewParams struct {
	// Offset is the time offset (e.g., "+5m", "-1h", "+30s", "-2d").
	// Positive = future, Negative = past.
	Offset string

	// DisableNTP blocks NTP traffic to prevent time correction.
	DisableNTP bool
}

// ClockSkewWrapper wraps clock skew injection.
type ClockSkewWrapper struct {
	dockerClient DockerClient
}

// DockerClient interface for Docker operations.
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// New creates a new clock skew wrapper.
func New(dockerClient DockerClient) *ClockSkewWrapper {
	return &ClockSkewWrapper{dockerClient: dockerClient}
}

// allowHostClockSkewEnv is the env var an operator must set to opt in to the
// host-clock blast radius. The guard is here, not in the validator, so that
// fast-feedback unit tests exercising ValidateClockSkewParams don't need to
// set the env.
const allowHostClockSkewEnv = "CHAOS_ALLOW_HOST_CLOCK_SKEW"

// InjectClockSkew manipulates the system clock in a container.
//
// Because containers share the host kernel clock, this shifts time for every
// container on the host. The function refuses to run unless the operator has
// explicitly set CHAOS_ALLOW_HOST_CLOCK_SKEW=1.
func (cw *ClockSkewWrapper) InjectClockSkew(ctx context.Context, targetContainerID string, params ClockSkewParams) error {
	if os.Getenv(allowHostClockSkewEnv) != "1" {
		return fmt.Errorf("clock_skew refused: set %s=1 to opt in to host-wide clock skew (the mechanism uses `date -s`, which changes time for ALL containers on the host — only use in isolated dev environments)", allowHostClockSkewEnv)
	}

	log.Warn().
		Str("container", targetContainerID[:12]).
		Str("offset", params.Offset).
		Bool("disable_ntp", params.DisableNTP).
		Msg("clock_skew uses host kernel date -s — affects ALL containers on host; use only in isolated dev environments")

	fmt.Printf("Injecting clock skew on target %s\n", targetContainerID[:12])

	// Save the offset (not the absolute epoch) so restore can compensate for
	// elapsed wall-clock time. The previous approach stored `date +%s` at
	// inject and `date -s "@$saved"` at restore, snapping the clock back to
	// inject-time regardless of how long the fault had run. On a 10-minute
	// scenario with +5m skew, restore would leave the clock ~10 minutes in
	// the past instead of the host's current time. Storing the offset and
	// subtracting it at restore makes the restoration correct.
	offsetSec, err := parseOffsetToSeconds(params.Offset)
	if err != nil {
		return fmt.Errorf("invalid offset %q: %w", params.Offset, err)
	}
	saveCmd := []string{"sh", "-c", fmt.Sprintf("echo %d > /tmp/.chaos_clock_offset", offsetSec)}
	if _, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, saveCmd); err != nil {
		log.Warn().Err(err).Str("container", targetContainerID[:12]).Msg("could not save clock offset")
	}

	if params.DisableNTP {
		ntpCmd := []string{"sh", "-c", "iptables -A OUTPUT -p udp --dport 123 -j DROP -m comment --comment chaos-ntp-block 2>/dev/null || true"}
		if _, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, ntpCmd); err != nil {
			log.Warn().Err(err).Str("container", targetContainerID[:12]).Msg("failed to block NTP traffic")
		}
		fmt.Printf("  NTP traffic blocked\n")
	}

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

	verifyCmd := []string{"date"}
	newTime, _ := cw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
	fmt.Printf("  Clock skewed by %s (%+ds) — new time: %s\n", params.Offset, offsetSec, strings.TrimSpace(newTime))

	return nil
}

// RemoveClockSkew restores the original clock and unconditionally attempts to
// remove the NTP-block iptables rule. The rule-delete is idempotent: `|| true`
// swallows "no matching rule" when DisableNTP was never set, and actually
// removes the rule when it was — closing the leak where RemoveFault used to
// skip the unblock because it received zero-valued params from the dispatcher.
func (cw *ClockSkewWrapper) RemoveClockSkew(ctx context.Context, targetContainerID string, _ ClockSkewParams) error {
	fmt.Printf("Restoring clock on target %s\n", targetContainerID[:12])

	ntpCmd := []string{"sh", "-c", "iptables -D OUTPUT -p udp --dport 123 -j DROP -m comment --comment chaos-ntp-block 2>/dev/null || true"}
	if _, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, ntpCmd); err != nil {
		log.Warn().Err(err).Str("container", targetContainerID[:12]).Msg("failed to remove chaos-ntp-block rule")
	}

	// Restore by subtracting the recorded offset from the CURRENT clock, not
	// by snapping back to the inject-time epoch. This preserves the elapsed
	// wall-clock duration of the fault window.
	//
	// Backward-compat: old `.chaos_original_time` (absolute epoch) artifacts
	// from pre-fix runs are still honored, since restoring to a slightly
	// stale epoch is still safer than leaving the skew in place.
	restoreCmd := []string{"sh", "-c", `
		if [ -f /tmp/.chaos_clock_offset ]; then
			OFFSET=$(cat /tmp/.chaos_clock_offset)
			NEW_EPOCH=$(( $(date +%s) - OFFSET ))
			date -s "@$NEW_EPOCH" 2>/dev/null || true
			rm -f /tmp/.chaos_clock_offset
		elif [ -f /tmp/.chaos_original_time ]; then
			date -s "@$(cat /tmp/.chaos_original_time)" 2>/dev/null || true
			rm -f /tmp/.chaos_original_time
		fi
	`}
	if _, err := cw.dockerClient.ExecCommand(ctx, targetContainerID, restoreCmd); err != nil {
		fmt.Printf("  Warning: Clock restoration may be imprecise: %v\n", err)
	}

	fmt.Printf("  Clock restored on target %s\n", targetContainerID[:12])
	return nil
}

// parseOffsetToSeconds converts offsets like "+5m", "-1h", "+30s" to seconds.
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

// ValidateClockSkewParams validates clock skew parameters.
func ValidateClockSkewParams(params ClockSkewParams) error {
	if params.Offset == "" {
		return fmt.Errorf("offset must be specified (e.g., '+5m', '-1h', '+30s')")
	}
	if _, err := parseOffsetToSeconds(params.Offset); err != nil {
		return fmt.Errorf("invalid offset: %w", err)
	}
	return nil
}
