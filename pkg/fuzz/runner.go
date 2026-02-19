package fuzz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/config"
	"github.com/jihwankim/chaos-utils/pkg/core/orchestrator"
	"github.com/jihwankim/chaos-utils/pkg/reporting"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
	"gopkg.in/yaml.v3"
)

// TriggerCondition describes a Prometheus-detectable protocol state to wait for
// before injecting a fault, landing faults at interesting protocol moments.
type TriggerCondition struct {
	Name        string
	Description string
	Query       string // PromQL instant query; fires when result > 0
}

// Triggers is the catalogue of available injection-timing conditions.
var Triggers = map[string]TriggerCondition{
	"any": {
		Name:        "any",
		Description: "Inject immediately — no Prometheus condition required.",
		Query:       "",
	},
	"checkpoint": {
		Name:        "checkpoint",
		Description: "Wait for active Heimdall checkpoint signing before injecting.",
		Query:       `increase(heimdallv2_checkpoint_api_calls_total[2m]) > 0`,
	},
	"post_restart": {
		Name:        "post_restart",
		Description: "Wait until just after a service restart (recovery stress).",
		Query:       `changes(up{job=~"l2-.*"}[5m]) > 0`,
	},
	"high_load": {
		Name:        "high_load",
		Description: "Wait for sustained Bor block production before injecting.",
		Query:       `rate(chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}[1m]) > 0.3`,
	},
}

// RoundResult is one entry in the JSONL run log.
type RoundResult struct {
	Session   string      `json:"session"`
	Seed      int64       `json:"seed"`
	Round     int         `json:"round"`
	Name      string      `json:"name"`
	Faults    []FaultSpec `json:"faults"`
	Trigger   string      `json:"trigger"`
	Result    string      `json:"result"` // "passed" | "failed" | "dry-run"
	ElapsedS  float64     `json:"elapsed_s"`
	Timestamp string      `json:"timestamp"`
}

// Config holds all settings for a fuzz session.
type Config struct {
	Enclave      string
	Rounds       int
	CompoundBias float64
	CompoundOnly bool
	SingleOnly   bool
	MaxFaults    int   // maximum simultaneous faults per compound scenario (default 2)
	Trigger      string
	Seed         int64 // 0 = auto-generate
	DryRun       bool
	LogPath      string
}

// Runner executes randomized fuzz rounds against a live Kurtosis enclave.
type Runner struct {
	cfg           *Config
	appCfg        *config.Config
	logger        *reporting.Logger
	prometheusURL string
}

// NewRunner builds a Runner, discovering Prometheus when a trigger condition is set.
func NewRunner(cfg *Config, appCfg *config.Config, logger *reporting.Logger) *Runner {
	r := &Runner{cfg: cfg, appCfg: appCfg, logger: logger}

	if cfg.Trigger != "any" && !cfg.DryRun {
		if appCfg.Prometheus.URL != "" && appCfg.Prometheus.URL != "http://localhost:9090" {
			r.prometheusURL = appCfg.Prometheus.URL
		} else if u, err := config.DiscoverPrometheusEndpoint(cfg.Enclave); err == nil {
			r.prometheusURL = u
			logger.Info("Auto-discovered Prometheus", "url", u)
		} else {
			logger.Warn("Could not discover Prometheus — trigger will be skipped", "error", err)
		}
	}

	return r
}

// Run executes cfg.Rounds fuzz rounds sequentially, logging each to cfg.LogPath.
func (r *Runner) Run(ctx context.Context) error {
	seed := r.cfg.Seed
	if seed == 0 {
		seed = rand.Int63() //nolint:gosec
	}
	sampler := NewSampler(seed)

	bias := r.cfg.CompoundBias
	maxFaults := r.cfg.MaxFaults
	switch {
	case r.cfg.CompoundOnly:
		bias = 1.0
	case r.cfg.SingleOnly:
		bias = 0.0
		maxFaults = 1
	}
	if maxFaults < 1 {
		maxFaults = 2 // default: up to 2 simultaneous faults
	}

	sessionID := time.Now().Format(time.RFC3339)
	fmt.Printf("Seed: %d  (pass --seed %d to reproduce)\n\n", seed, seed)
	fmt.Printf("Starting %d fuzz rounds  (compound_bias=%.0f%%, trigger=%s)\n",
		r.cfg.Rounds, bias*100, r.cfg.Trigger)
	fmt.Println(strings.Repeat("─", 72))

	passed, failed := 0, 0

	interrupted := false

	for round := 1; round <= r.cfg.Rounds; round++ {
		if ctx.Err() != nil {
			interrupted = true
			break
		}

		specs := sampler.Sample(bias, maxFaults)
		sc, name := BuildScenario(specs, r.cfg.Enclave)

		kind := "single "
		if len(specs) > 1 {
			kind = "compound"
		}
		fmt.Printf("\n[%d/%d] %s  %s\n", round, r.cfg.Rounds, kind, name)
		for _, spec := range specs {
			fmt.Printf("         %-32s → %s\n", spec.Slug, TargetTiers[spec.TargetTier].Label)
		}

		if r.cfg.DryRun {
			fmt.Println("  (dry-run)")
			r.appendLog(sessionID, seed, round, name, specs, "dry-run", 0)
			continue
		}

		if r.cfg.Trigger != "any" && r.prometheusURL != "" {
			r.waitForTrigger(ctx, r.cfg.Trigger)
		}

		start := time.Now()
		success, runErr := r.execute(ctx, sc)
		elapsed := time.Since(start).Seconds()

		// If the context was cancelled mid-round, the orchestrator already ran
		// cleanup via its emergency controller. Log the round and stop the loop.
		if ctx.Err() != nil {
			r.appendLog(sessionID, seed, round, name, specs, "interrupted", elapsed)
			interrupted = true
			break
		}

		status := "passed"
		if !success || runErr != nil {
			status = "failed"
			if runErr != nil {
				r.logger.Error("Round execution error", "round", round, "error", runErr)
			}
		}
		fmt.Printf("\n  → %s  (%.0fs)\n", strings.ToUpper(status), elapsed)

		if success && runErr == nil {
			passed++
		} else {
			failed++
		}

		r.appendLog(sessionID, seed, round, name, specs, status, elapsed)
	}

	fmt.Println("\n" + strings.Repeat("─", 72))
	if interrupted {
		fmt.Printf("Interrupted.  %d passed  %d failed  (seed=%d)\n", passed, failed, seed)
	} else {
		fmt.Printf("Done.  %d passed  %d failed  (seed=%d)\n", passed, failed, seed)
	}
	if failed > 0 {
		fmt.Printf("\nReproduce: chaos-runner fuzz --enclave %s --seed %d --rounds %d\n",
			r.cfg.Enclave, seed, r.cfg.Rounds)
	}
	fmt.Printf("Log: %s\n", r.cfg.LogPath)
	return nil
}

// execute marshals sc to a temp YAML file, runs it through the orchestrator,
// removes the temp file, and returns (success, error).
func (r *Runner) execute(ctx context.Context, sc *scenario.Scenario) (bool, error) {
	data, err := yaml.Marshal(sc)
	if err != nil {
		return false, fmt.Errorf("marshal scenario: %w", err)
	}

	f, err := os.CreateTemp("", "chaos-fuzz-*.yaml")
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, fmt.Errorf("write temp file: %w", err)
	}
	f.Close()

	orch, err := orchestrator.New(r.appCfg)
	if err != nil {
		return false, fmt.Errorf("create orchestrator: %w", err)
	}

	result, err := orch.Execute(ctx, tmpPath)
	if err != nil {
		return false, err
	}
	return result.Success, nil
}

// waitForTrigger polls Prometheus until the named trigger fires or a 5-minute
// timeout expires.
func (r *Runner) waitForTrigger(ctx context.Context, triggerName string) {
	trigger, ok := Triggers[triggerName]
	if !ok || trigger.Query == "" {
		return
	}
	r.logger.Info("Waiting for trigger", "name", triggerName, "description", trigger.Description)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		if val, err := r.queryPrometheus(trigger.Query); err == nil && val > 0 {
			r.logger.Info("Trigger fired", "name", triggerName, "value", val)
			return
		}
		time.Sleep(15 * time.Second)
	}
	r.logger.Warn("Trigger timed out — injecting anyway", "name", triggerName)
}

// queryPrometheus runs a PromQL instant query and returns the first scalar result.
func (r *Runner) queryPrometheus(query string) (float64, error) {
	endpoint := fmt.Sprintf("%s/api/v1/query?%s",
		r.prometheusURL,
		url.Values{"query": {query}}.Encode(),
	)
	resp, err := http.Get(endpoint) //nolint:noctx
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Data struct {
			Result []struct {
				Value [2]interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if len(result.Data.Result) == 0 {
		return 0, fmt.Errorf("empty result set")
	}
	s, ok := result.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value type in Prometheus response")
	}
	var val float64
	if _, err := fmt.Sscanf(s, "%f", &val); err != nil {
		return 0, err
	}
	return val, nil
}

// appendLog appends a RoundResult entry to the JSONL log file.
func (r *Runner) appendLog(session string, seed int64, round int, name string, specs []FaultSpec, result string, elapsed float64) {
	entry := RoundResult{
		Session:   session,
		Seed:      seed,
		Round:     round,
		Name:      name,
		Faults:    specs,
		Trigger:   r.cfg.Trigger,
		Result:    result,
		ElapsedS:  math.Round(elapsed*10) / 10,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if err := os.MkdirAll(filepath.Dir(r.cfg.LogPath), 0755); err != nil {
		r.logger.Warn("Failed to create log dir", "error", err)
		return
	}

	f, err := os.OpenFile(r.cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		r.logger.Warn("Failed to open log file", "error", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = f.WriteString(string(data) + "\n")
}
