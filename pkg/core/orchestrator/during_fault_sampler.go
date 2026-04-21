package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/monitoring/detector"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// duringFaultSampler polls during_fault success criteria on a fixed interval
// while faults are being injected and monitored. It exists because some
// fault injectors (e.g. container_pause with Duration set) block their
// InjectFault call for the full fault window and self-unpause before the
// fault leaves INJECT, so a single end-of-MONITOR evaluation observes
// post-fault state instead of the faulted state. The sampler runs
// concurrently with INJECT + MONITOR and records the WORST reading it sees
// per criterion, so a failure anywhere in the window is preserved.
//
// "Worst" here means: if the criterion ever failed, the failed reading
// wins over any later passing reading. This is conservative — during_fault
// criteria are about proving the fault was effective, and a single
// non-effective reading means the fault wasn't observable.
type duringFaultSampler struct {
	detector   *detector.FailureDetector
	criteria   []scenario.SuccessCriterion
	indices    []int // indices into original scenario.SuccessCriteria for during_fault subset
	interval   time.Duration

	mu      sync.Mutex
	results map[string]*detector.CriterionResult // criterion name → worst reading
	samples int                                  // total sample rounds completed (for reporting)
	skipped map[string]int                       // counts of samples skipped due to eval errors (log criteria pre log-context wire-up, etc.)

	cancel context.CancelFunc
	done   chan struct{}
}

// newDuringFaultSampler constructs (but does not start) a sampler for all
// criteria marked during_fault in the scenario spec. If there are no
// during_fault criteria the returned sampler is a no-op.
func newDuringFaultSampler(det *detector.FailureDetector, criteria []scenario.SuccessCriterion, interval time.Duration) *duringFaultSampler {
	s := &duringFaultSampler{
		detector: det,
		criteria: criteria,
		interval: interval,
		results:  make(map[string]*detector.CriterionResult),
		skipped:  make(map[string]int),
		done:     make(chan struct{}),
	}
	for i, c := range criteria {
		if c.DuringFault {
			s.indices = append(s.indices, i)
		}
	}
	return s
}

// Start launches the sampling goroutine. Safe to call with no during_fault
// criteria — the goroutine simply closes its done channel and exits.
func (s *duringFaultSampler) Start(parentCtx context.Context) {
	if len(s.indices) == 0 || s.detector == nil {
		close(s.done)
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	s.cancel = cancel

	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		// Warmup: wait one scrape cycle (~15s Prom default) for injected
		// faults to have an observable effect before first sample. Without
		// this, the first sample likely shows pre-fault values.
		warmup := time.After(15 * time.Second)

		for {
			select {
			case <-ctx.Done():
				return
			case <-warmup:
				s.sampleOnce(ctx)
				warmup = nil
			case <-ticker.C:
				s.sampleOnce(ctx)
			}
		}
	}()
}

// sampleOnce evaluates each during_fault criterion and keeps the worst.
func (s *duringFaultSampler) sampleOnce(ctx context.Context) {
	for _, idx := range s.indices {
		c := s.criteria[idx]
		r, err := s.detector.EvaluateOnce(ctx, c)
		if err != nil {
			s.mu.Lock()
			s.skipped[c.Name]++
			s.mu.Unlock()
			continue
		}

		s.mu.Lock()
		prev, ok := s.results[c.Name]
		// Replace if: no prior sample OR the new reading is worse (failed
		// where prior passed). Passed readings do NOT overwrite a failed
		// prior reading — that would silently discard the evidence of the
		// fault being observable at some point in the window.
		if !ok || (prev.Passed && !r.Passed) {
			s.results[c.Name] = r
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.samples++
	s.mu.Unlock()
}

// Stop cancels the sampling goroutine and waits for it to exit.
func (s *duringFaultSampler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

// StopAndCollect stops the sampler and returns a snapshot of worst-observed
// results keyed by criterion name.
func (s *duringFaultSampler) StopAndCollect() map[string]*detector.CriterionResult {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*detector.CriterionResult, len(s.results))
	for k, v := range s.results {
		out[k] = v
	}
	if len(s.skipped) > 0 {
		// Surface skipped-sample counts so operators can see when a
		// criterion repeatedly failed to evaluate (e.g. log context not
		// yet wired, query syntax error, Prom down).
		for name, n := range s.skipped {
			fmt.Printf("    [during-fault-sampler] note: %d sample(s) for %q were skipped due to eval errors\n", n, name)
		}
	}
	return out
}

// SampleCount reports how many sample rounds the sampler completed.
func (s *duringFaultSampler) SampleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.samples
}
