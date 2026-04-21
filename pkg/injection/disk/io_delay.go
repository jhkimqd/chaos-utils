package disk

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// IODelayParams defines parameters for disk I/O delay injection.
// Only Method="dd" (default) is implemented; Method="dm-delay" is parsed so
// scenarios using it fail loudly via ValidateIODelayParams rather than silently
// falling through to dd.
type IODelayParams struct {
	// IOLatencyMs controls contention intensity via dd worker-count scaling
	// (<100ms=1 worker, 100-199=2, 200+=3), not precise per-I/O latency.
	IOLatencyMs int

	// TargetPath is the directory where contention files are written
	// (e.g., "/var/lib/bor/bor/chaindata").
	TargetPath string

	// Operation is the operation type: "read", "write", or "all".
	Operation string

	// Method selects the injection approach. Only "dd" (the default) and ""
	// are accepted. Passing "dm-delay" results in a validation error; see
	// dm_delay.go for why that mode is unsupported in this framework.
	Method string
}

// DockerClient interface for Docker operations
type DockerClient interface {
	ExecCommand(ctx context.Context, containerID string, cmd []string) (string, error)
}

// IODelayWrapper wraps disk I/O delay injection
type IODelayWrapper struct {
	dockerClient DockerClient
	dmDelay      *DmDelayWrapper

	// injectedPaths tracks the TargetPath supplied at InjectIODelay time so
	// RemoveFault can scrub the correct directory even when the orchestrator
	// passes an empty IODelayParams at teardown.
	mu            sync.Mutex
	injectedPaths map[string]string
}

// New creates a new I/O delay wrapper
func New(dockerClient DockerClient) *IODelayWrapper {
	return &IODelayWrapper{
		dockerClient:  dockerClient,
		dmDelay:       NewDmDelayWrapper(dockerClient),
		injectedPaths: make(map[string]string),
	}
}

// workerCount maps IOLatencyMs intensity to worker count: <100=1, 100-199=2, 200+=3.
func workerCount(ioLatencyMs int) int {
	switch {
	case ioLatencyMs >= 200:
		return 3
	case ioLatencyMs >= 100:
		return 2
	default:
		return 1
	}
}

// InjectIODelay creates I/O contention by running background dd processes that
// saturate the I/O queue. Each worker shell's PID is written to a pidfile; the
// verification step reads that pidfile and checks `kill -0` on every PID, so
// the result is deterministic rather than pattern-matched against /proc.
// The "dm-delay" method is intentionally rejected — see dm_delay.go for details.
func (iw *IODelayWrapper) InjectIODelay(ctx context.Context, targetContainerID string, params IODelayParams) error {
	if params.Method == "dm-delay" {
		return iw.dmDelay.InjectDmDelay(ctx, targetContainerID, params)
	}

	fmt.Printf("Injecting I/O contention on target %s\n", targetContainerID[:12])

	targetPath := params.TargetPath
	if targetPath == "" {
		targetPath = "/tmp"
	}

	workers := workerCount(params.IOLatencyMs)

	base := strings.TrimRight(targetPath, "/")
	chaosFile := base + "/.chaos_io_stress"
	pidFile := base + "/.chaos_io_stress.pids"

	// Build the spawn block(s) once per operation. Each backgrounded worker
	// is a subshell that fully detaches its stdio BEFORE it runs anything —
	// without the `</dev/null >/dev/null 2>&1` redirection the inherited
	// fds keep docker exec's attach stream open and StdCopy blocks for the
	// full fault window. After `( ... ) & `, $! is the PID of the worker
	// subshell; we append it to $PIDFILE so RemoveFault can kill by PID.
	var spawnBlock string
	switch params.Operation {
	case "write":
		spawnBlock = fmt.Sprintf(
			"for i in $(seq 1 %d); do "+
				"( while true; do dd if=/dev/zero of=\"%s_$i\" bs=64K count=256 conv=fdatasync 2>/dev/null; done ) </dev/null >/dev/null 2>&1 & "+
				"echo $! >> \"$PIDFILE\"; "+
				"done",
			workers, chaosFile,
		)
	case "read":
		spawnBlock = fmt.Sprintf(
			"dd if=/dev/zero of=\"%s_src\" bs=1M count=16 2>/dev/null; "+
				"for i in $(seq 1 %d); do "+
				"( while true; do dd if=\"%s_src\" of=/dev/null bs=64K 2>/dev/null; done ) </dev/null >/dev/null 2>&1 & "+
				"echo $! >> \"$PIDFILE\"; "+
				"done",
			chaosFile, workers, chaosFile,
		)
	default: // "all"
		spawnBlock = fmt.Sprintf(
			"dd if=/dev/zero of=\"%s_src\" bs=1M count=16 2>/dev/null; "+
				"for i in $(seq 1 %d); do "+
				"( while true; do dd if=/dev/zero of=\"%s_w$i\" bs=64K count=256 conv=fdatasync 2>/dev/null; done ) </dev/null >/dev/null 2>&1 & "+
				"echo $! >> \"$PIDFILE\"; "+
				"done; "+
				"for i in $(seq 1 %d); do "+
				"( while true; do dd if=\"%s_src\" of=/dev/null bs=64K 2>/dev/null; done ) </dev/null >/dev/null 2>&1 & "+
				"echo $! >> \"$PIDFILE\"; "+
				"done",
			chaosFile, workers, chaosFile, workers, chaosFile,
		)
	}

	// One shell invocation owns the full contract: spawn workers, wait for
	// dd to actually exec, then report the tuple `alive total` read from
	// the pidfile. `sleep 0.3` gives dd time to fork+exec; BusyBox ashes
	// that reject fractional sleeps fall back to `sleep 1`. The outer
	// shell's fds stay attached to docker exec so we receive the output,
	// but all workers have already detached — exec returns the instant
	// the outer shell reaches its final `echo`.
	startScript := fmt.Sprintf(
		"PIDFILE=%q; "+
			": > \"$PIDFILE\"; "+
			"%s; "+
			"sleep 0.3 2>/dev/null || sleep 1; "+
			"ALIVE=0; TOTAL=0; "+
			"while IFS= read -r pid; do "+
			"[ -z \"$pid\" ] && continue; "+
			"TOTAL=$((TOTAL+1)); "+
			"if kill -0 \"$pid\" 2>/dev/null; then ALIVE=$((ALIVE+1)); fi; "+
			"done < \"$PIDFILE\"; "+
			"echo \"$ALIVE $TOTAL\"",
		pidFile, spawnBlock,
	)

	out, err := iw.dockerClient.ExecCommand(ctx, targetContainerID, []string{"sh", "-c", startScript})
	if err != nil {
		return fmt.Errorf("failed to start I/O contention: %w", err)
	}

	alive, total, parseErr := parseAliveTotal(out)
	if parseErr != nil {
		return fmt.Errorf("unexpected I/O contention start output %q: %w", strings.TrimSpace(out), parseErr)
	}
	if total == 0 {
		return fmt.Errorf("I/O contention failed: no workers spawned (pidfile empty)")
	}
	if alive == 0 {
		return fmt.Errorf("I/O contention failed: all %d worker(s) exited before verification (check disk permissions/space on %s)", total, targetPath)
	}
	if alive < total {
		// Partial success — inject worked but some workers died. Log and
		// continue; the surviving workers still generate contention.
		log.Warn().
			Int("alive", alive).
			Int("total", total).
			Str("container", targetContainerID[:12]).
			Str("path", targetPath).
			Msg("some I/O contention workers exited before verification; continuing with survivors")
	}

	iw.mu.Lock()
	iw.injectedPaths[targetContainerID] = targetPath
	iw.mu.Unlock()

	fmt.Printf("  I/O contention active: %d/%d workers on %s (%s)\n", alive, total, targetPath, params.Operation)
	return nil
}

// RemoveFault kills the worker shells recorded at inject time, sweeps any
// orphaned processes carrying the chaos marker, and deletes stress files.
func (iw *IODelayWrapper) RemoveFault(ctx context.Context, targetContainerID string, params IODelayParams) error {
	fmt.Printf("Removing I/O contention from target %s\n", targetContainerID[:12])

	// Resolve the target path: caller-provided > inject-time record > /tmp.
	targetPath := params.TargetPath
	if targetPath == "" {
		iw.mu.Lock()
		if p, ok := iw.injectedPaths[targetContainerID]; ok {
			targetPath = p
		}
		iw.mu.Unlock()
	}
	if targetPath == "" {
		targetPath = "/tmp"
	}
	base := strings.TrimRight(targetPath, "/")
	chaosFile := base + "/.chaos_io_stress"
	pidFile := base + "/.chaos_io_stress.pids"

	// Kill by recorded PID first (authoritative), then sweep any survivors
	// carrying the chaos_io_stress marker. The sweep catches dd children
	// that were mid-exec when their parent shell was killed and any process
	// left from a prior crashed run where the pidfile went missing.
	//
	// Two subtleties that bit earlier revisions:
	//  1. The remove script's own cmdline contains the chaos_io_stress
	//     marker (the pidfile path is an argument). Without a self-PID
	//     guard the sweep's `kill -9` would SIGKILL the very shell running
	//     it (exit 137, RemoveFault reports bogus "still running").
	//  2. `tr ... < "$p"` has sh evaluate the redirection before tr runs,
	//     so if the /proc entry vanishes between glob-expand and read,
	//     sh (not tr) prints "can't open …". `2>/dev/null` on tr doesn't
	//     suppress it; wrap the whole compound in a { …; } 2>/dev/null.
	removeScript := fmt.Sprintf(
		"MY_PID=$$; "+
			"PIDFILE=%q; "+
			"if [ -f \"$PIDFILE\" ]; then "+
			"while IFS= read -r pid; do "+
			"[ -z \"$pid\" ] && continue; "+
			"[ \"$pid\" = \"$MY_PID\" ] && continue; "+
			"kill -9 \"$pid\" 2>/dev/null; "+
			"done < \"$PIDFILE\"; "+
			"fi; "+
			"for p in /proc/[0-9]*/cmdline; do "+
			"PID=$(basename \"$(dirname \"$p\")\"); "+
			"[ \"$PID\" = \"$MY_PID\" ] && continue; "+
			"CMD=$({ tr '\\0' ' ' < \"$p\"; } 2>/dev/null); "+
			"case \"$CMD\" in "+
			"*chaos_io_stress*) kill -9 \"$PID\" 2>/dev/null ;; "+
			"esac; "+
			"done; "+
			"rm -f \"$PIDFILE\" \"%s\"_* 2>/dev/null; "+
			"find /tmp /root /var/lib -maxdepth 6 -name '.chaos_io_stress_*' -delete 2>/dev/null; "+
			"echo done",
		pidFile, chaosFile,
	)

	_, killErr := iw.dockerClient.ExecCommand(ctx, targetContainerID, []string{"sh", "-c", removeScript})
	if killErr != nil {
		log.Warn().Err(killErr).Str("container", targetContainerID[:12]).Msg("failed to run I/O contention removal script")
	}

	// Always verify no chaos_io_stress-tagged processes remain — the remove
	// script swallows individual kill/rm errors via 2>/dev/null, so killErr
	// being nil doesn't prove success. Same self-skip + redirection-error
	// caveats as the remove script.
	verifyCmd := []string{"sh", "-c",
		"MY_PID=$$; COUNT=0; for p in /proc/[0-9]*/cmdline; do " +
			"PID=$(basename \"$(dirname \"$p\")\"); " +
			"[ \"$PID\" = \"$MY_PID\" ] && continue; " +
			"CMD=$({ tr '\\0' ' ' < \"$p\"; } 2>/dev/null); " +
			"case \"$CMD\" in *chaos_io_stress*) COUNT=$((COUNT+1)) ;; esac; " +
			"done; echo $COUNT",
	}
	countOutput, verifyErr := iw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
	if verifyErr == nil {
		count := strings.TrimSpace(countOutput)
		if count != "" && count != "0" {
			return fmt.Errorf("failed to remove I/O contention: %s processes still running after kill", count)
		}
	} else {
		log.Warn().Err(verifyErr).Str("container", targetContainerID[:12]).Msg("could not verify I/O contention removal")
	}

	iw.mu.Lock()
	delete(iw.injectedPaths, targetContainerID)
	iw.mu.Unlock()

	fmt.Printf("  I/O contention removed from target %s\n", targetContainerID[:12])
	return nil
}

// parseAliveTotal parses the "ALIVE TOTAL" tuple the start script emits.
func parseAliveTotal(out string) (alive, total int, err error) {
	trimmed := strings.TrimSpace(out)
	// The script emits exactly one line; tolerate prior warnings by taking
	// the last non-empty line.
	lines := strings.Split(trimmed, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			last = s
			break
		}
	}
	if last == "" {
		return 0, 0, fmt.Errorf("empty output")
	}
	if _, err := fmt.Sscanf(last, "%d %d", &alive, &total); err != nil {
		return 0, 0, err
	}
	return alive, total, nil
}

// ValidateIODelayParams validates I/O delay parameters
func ValidateIODelayParams(params IODelayParams) error {
	if params.IOLatencyMs < 0 {
		return fmt.Errorf("io_latency_ms cannot be negative")
	}

	if params.Operation != "read" && params.Operation != "write" && params.Operation != "all" {
		return fmt.Errorf("operation must be 'read', 'write', or 'all'")
	}

	switch params.Method {
	case "", "dd":
		// ok
	case "dm-delay":
		return ErrDmDelayUnsupported
	default:
		return fmt.Errorf("unsupported method %q; valid values: 'dd' or '' (empty)", params.Method)
	}

	return nil
}
