// Package logcollector fetches and filters container logs for fault-injected
// services after a failed chaos test. Collection is always best-effort: any
// error is silently ignored so that log capture never blocks or fails a test.
package logcollector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// errPattern matches log lines that indicate error-level events.
// Intentionally excludes WARN — warn lines are too noisy in Bor/Heimdall (e.g.
// "Demoting invalidated transaction" floods on every reorg) and mask real errors.
// Covers Bor glog format ("ERROR", "CRIT") and Cosmos SDK JSON ("level":"error").
var errPattern = regexp.MustCompile(`(?i)\b(error|crit|panic|fatal)\b`)

// ServiceLogSnapshot captures log excerpts from one container after a failure.
type ServiceLogSnapshot struct {
	ServiceName string    // human-readable name (Docker container name)
	ContainerID string    // full Docker container ID
	ErrorLines  []string  // lines matching error/crit/panic/fatal (WARN excluded)
	TailLines   []string  // last N raw lines (full context window)
	CapturedAt  time.Time
	Since       time.Time // start of the captured window (= fault injection time)
}

// Collector fetches container logs using the Docker API.
type Collector struct {
	dockerClient *docker.Client
}

// New creates a Collector backed by the given Docker client.
func New(dockerClient *docker.Client) *Collector {
	return &Collector{dockerClient: dockerClient}
}

// Snapshot fetches the last tailN log lines for a container since the given
// time, filters for error-level entries, and returns a snapshot.
// Returns nil if the container is unreachable or produces no output.
// A hard 10-second internal timeout prevents long hangs on unresponsive daemons.
func (c *Collector) Snapshot(ctx context.Context, containerID, serviceName string, since time.Time, tailN int) *ServiceLogSnapshot {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	lines, err := c.dockerClient.ContainerLogs(tctx, containerID, tailN, since)
	if err != nil || len(lines) == 0 {
		return nil
	}

	var errLines []string
	for _, l := range lines {
		if errPattern.MatchString(l) {
			errLines = append(errLines, l)
		}
	}

	return &ServiceLogSnapshot{
		ServiceName: serviceName,
		ContainerID: containerID,
		ErrorLines:  errLines,
		TailLines:   lines,
		CapturedAt:  time.Now(),
		Since:       since,
	}
}

// PrintSummary prints a compact error-line digest for each snapshot to stdout.
// Only ERROR/CRIT/PANIC/FATAL lines are shown; WARN is intentionally excluded.
// If a service produced no error-level lines, a brief "no errors" note is shown.
func PrintSummary(snapshots []*ServiceLogSnapshot) {
	if len(snapshots) == 0 {
		return
	}
	sep := strings.Repeat("─", 72)
	fmt.Println("\n[LOG DIGEST] ERROR/CRIT/PANIC/FATAL lines from fault window (WARN excluded)")
	for _, s := range snapshots {
		fmt.Printf("%s\n  %s\n  fault window: %s → %s\n%s\n",
			sep, s.ServiceName,
			s.Since.Format("15:04:05"), s.CapturedAt.Format("15:04:05"),
			sep)
		if len(s.ErrorLines) == 0 {
			fmt.Println("  (no ERROR/CRIT/PANIC/FATAL lines during fault window — check tail log for context)")
		} else {
			// Print at most 30 error lines to avoid flooding the terminal.
			limit := 30
			for i, l := range s.ErrorLines {
				if i >= limit {
					fmt.Printf("  ... (%d more lines — see full log in reports/)\n",
						len(s.ErrorLines)-limit)
					break
				}
				fmt.Printf("  %s\n", l)
			}
		}
	}
	fmt.Println(sep)
}

// Save writes each snapshot to <dir>/<serviceName>.errors.log (filtered lines)
// and <dir>/<serviceName>.tail.log (full context window). Silently skips on
// any file system error — saving logs must never fail a test.
func Save(snapshots []*ServiceLogSnapshot, dir string) {
	if len(snapshots) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	for _, s := range snapshots {
		safe := sanitizeFilename(s.ServiceName)

		if len(s.ErrorLines) > 0 {
			_ = os.WriteFile(
				filepath.Join(dir, safe+".errors.log"),
				[]byte(strings.Join(s.ErrorLines, "\n")+"\n"),
				0644,
			)
		}

		if len(s.TailLines) > 0 {
			_ = os.WriteFile(
				filepath.Join(dir, safe+".tail.log"),
				[]byte(strings.Join(s.TailLines, "\n")+"\n"),
				0644,
			)
		}
	}
	fmt.Printf("[LOG DIGEST] Full logs saved to: %s\n", dir)
}

// sanitizeFilename replaces characters that are unsafe in filenames.
func sanitizeFilename(name string) string {
	r := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return r.Replace(name)
}
