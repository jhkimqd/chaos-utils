package logcollector

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
)

// WatchTarget identifies a container to watch.
type WatchTarget struct {
	ContainerID string
	Name        string
}

// Watcher streams container logs in real-time and prints error/panic lines
// immediately to stdout. It is started alongside the metrics collector during
// the MONITOR phase so operators see problems as they happen.
type Watcher struct {
	dockerClient *docker.Client
	targets      []WatchTarget
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewWatcher creates a Watcher for the given targets.
func NewWatcher(dockerClient *docker.Client, targets []WatchTarget) *Watcher {
	return &Watcher{
		dockerClient: dockerClient,
		targets:      targets,
	}
}

// Start begins streaming logs from all targets. Each target gets its own
// goroutine. Errors/panics are printed to stdout as they appear.
func (w *Watcher) Start(ctx context.Context, since time.Time) {
	ctx, w.cancel = context.WithCancel(ctx)

	for _, t := range w.targets {
		t := t
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.watch(ctx, t, since)
		}()
	}
}

// Stop cancels all streaming goroutines and waits for them to finish.
func (w *Watcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// watch streams logs from a single container. Best-effort — any error quietly
// terminates the goroutine without affecting the test.
func (w *Watcher) watch(ctx context.Context, target WatchTarget, since time.Time) {
	reader, err := w.dockerClient.ContainerLogsFollow(ctx, target.ContainerID, since)
	if err != nil {
		return
	}
	defer reader.Close()

	// Docker multiplexes stdout/stderr with an 8-byte header per frame.
	// Pipe through stdcopy into a single reader we can scan line-by-line.
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, reader)
		pw.Close()
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if errPattern.MatchString(line) {
			// Trim to avoid excessively long lines flooding the terminal.
			display := strings.TrimRight(line, "\r")
			if len(display) > 300 {
				display = display[:300] + "..."
			}
			fmt.Printf("  [%s] [%s] %s\n",
				time.Now().Format("15:04:05"),
				target.Name,
				display,
			)
		}
	}
}
