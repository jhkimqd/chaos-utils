package process

import (
	"context"
	"fmt"
	"strings"
)

// KillParams defines parameters for process kill injection
type KillParams struct {
	// ProcessPattern is the pattern to match process names (e.g., "bor", "heimdalld")
	ProcessPattern string

	// Signal is the signal to send (default: "KILL")
	// Supported: HUP, INT, QUIT, KILL, TERM, USR1, USR2, STOP, CONT
	Signal string

	// KillChildren also kills child processes
	KillChildren bool

	// Interval is the delay in seconds between repeated kills (0 = single kill)
	Interval int

	// Count is the number of times to repeat the kill (default: 1)
	Count int
}

// InjectProcessKill kills a process by pattern within a container
func (pw *PriorityWrapper) InjectProcessKill(ctx context.Context, targetContainerID string, params KillParams) error {
	fmt.Printf("Injecting process kill on target %s (pattern: %s, signal: %s)\n",
		targetContainerID[:12], params.ProcessPattern, params.Signal)

	signal := params.Signal
	if signal == "" {
		signal = "KILL"
	}

	count := params.Count
	if count <= 0 {
		count = 1
	}

	for i := 0; i < count; i++ {
		if i > 0 && params.Interval > 0 {
			// Sleep between kills
			sleepCmd := []string{"sleep", fmt.Sprintf("%d", params.Interval)}
			_, _ = pw.dockerClient.ExecCommand(ctx, targetContainerID, sleepCmd)
		}

		// Find and kill processes matching pattern
		// Use /proc scanning for POSIX/BusyBox compatibility (pgrep/pkill may not exist)
		var killCmd []string
		if params.KillChildren {
			// Kill all matching processes
			killCmd = []string{"sh", "-c", fmt.Sprintf(
				"FOUND=0; for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); if tr '\\0' ' ' < $p 2>/dev/null | grep -q '%s'; then kill -%s $PID 2>/dev/null && FOUND=$((FOUND+1)); fi; done; echo \"killed $FOUND\"",
				params.ProcessPattern, signal,
			)}
		} else {
			// Kill only the first matching process
			killCmd = []string{"sh", "-c", fmt.Sprintf(
				"for p in /proc/[0-9]*/cmdline; do PID=$(echo $p | cut -d/ -f3); if tr '\\0' ' ' < $p 2>/dev/null | grep -q '%s'; then kill -%s $PID 2>/dev/null && echo \"killed $PID\" && exit 0; fi; done; echo 'no match'",
				params.ProcessPattern, signal,
			)}
		}

		output, err := pw.dockerClient.ExecCommand(ctx, targetContainerID, killCmd)
		if err != nil {
			// Process kill may "fail" because the exec connection drops when PID 1 dies
			// This is expected behavior — log and continue
			fmt.Printf("  Kill signal sent (exec may have disconnected): %v\n", err)
		} else {
			out := strings.TrimSpace(output)
			if out == "no match" {
				return fmt.Errorf("no process found matching pattern '%s'", params.ProcessPattern)
			}
			fmt.Printf("  Sent %s to '%s' (attempt %d/%d): %s\n",
				signal, params.ProcessPattern, i+1, count, out)
		}
	}

	return nil
}

// ValidateKillParams validates process kill parameters
func ValidateKillParams(params KillParams) error {
	if params.ProcessPattern == "" {
		return fmt.Errorf("process_pattern must be specified")
	}

	signal := strings.ToUpper(params.Signal)
	if signal == "" {
		signal = "KILL"
	}

	validSignals := map[string]bool{
		"HUP": true, "INT": true, "QUIT": true, "ILL": true,
		"TRAP": true, "ABRT": true, "KILL": true, "SEGV": true,
		"PIPE": true, "ALRM": true, "TERM": true, "USR1": true,
		"USR2": true, "STOP": true, "CONT": true,
	}

	if !validSignals[signal] {
		return fmt.Errorf("unsupported signal: %s", signal)
	}

	if params.Interval < 0 {
		return fmt.Errorf("interval cannot be negative")
	}

	return nil
}
