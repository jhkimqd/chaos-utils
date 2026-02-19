package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/jihwankim/chaos-utils/pkg/fuzz"
	"github.com/jihwankim/chaos-utils/pkg/reporting"
	"github.com/spf13/cobra"
)

var fuzzCmd = &cobra.Command{
	Use:   "fuzz",
	Args:  cobra.NoArgs,
	Short: "Run randomized chaos scenarios against a live enclave",
	Long: `Fuzz generates and executes randomized fault injection scenarios.

Parameters are sampled from near-threshold distributions (triangular/log-uniform)
biased toward values most likely to expose protocol bugs. Compound mode fires
multiple namespace-disjoint faults simultaneously for cross-component stress.

Triggers (--trigger):
  any           Inject immediately (default)
  checkpoint    Wait for active Heimdall checkpoint signing
  post_restart  Wait until just after a service restart
  high_load     Wait for sustained Bor block production

Examples:
  chaos-runner fuzz --enclave pos
  chaos-runner fuzz --enclave pos --rounds 20 --compound-only
  chaos-runner fuzz --enclave pos --trigger checkpoint
  chaos-runner fuzz --enclave pos --seed 42 --rounds 5 --dry-run`,
	RunE: runFuzz,
}

func init() {
	fuzzCmd.Flags().String("enclave", "", "Kurtosis enclave name (overrides config)")
	fuzzCmd.Flags().Int("rounds", 10, "number of fuzz rounds")
	fuzzCmd.Flags().Bool("compound-only", false, "only generate compound (multi-fault) scenarios")
	fuzzCmd.Flags().Bool("single-only", false, "only generate single-fault scenarios")
	fuzzCmd.Flags().Int("max-faults", 2, "max simultaneous faults in compound mode")
	fuzzCmd.Flags().String("trigger", "any", "wait for Prometheus condition before injecting (any|checkpoint|post_restart|high_load)")
	fuzzCmd.Flags().Int64("seed", 0, "random seed for reproducibility (0 = auto)")
	fuzzCmd.Flags().Bool("dry-run", false, "print scenarios without executing")
	fuzzCmd.Flags().String("log", "reports/fuzz_log.jsonl", "JSONL run log path")
}

func runFuzz(cmd *cobra.Command, _ []string) error {
	enclave, _ := cmd.Flags().GetString("enclave")
	rounds, _ := cmd.Flags().GetInt("rounds")
	compoundOnly, _ := cmd.Flags().GetBool("compound-only")
	singleOnly, _ := cmd.Flags().GetBool("single-only")
	maxFaults, _ := cmd.Flags().GetInt("max-faults")
	triggerName, _ := cmd.Flags().GetString("trigger")
	seed, _ := cmd.Flags().GetInt64("seed")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	logPath, _ := cmd.Flags().GetString("log")

	if compoundOnly && singleOnly {
		return fmt.Errorf("--compound-only and --single-only are mutually exclusive")
	}

	if _, ok := fuzz.Triggers[triggerName]; !ok {
		validNames := make([]string, 0, len(fuzz.Triggers))
		for n := range fuzz.Triggers {
			validNames = append(validNames, n)
		}
		sort.Strings(validNames)
		return fmt.Errorf("unknown trigger %q; valid: %s", triggerName, strings.Join(validNames, ", "))
	}

	appCfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if enclave != "" {
		appCfg.Kurtosis.EnclaveName = enclave
	}
	if !dryRun && appCfg.Kurtosis.EnclaveName == "" {
		return fmt.Errorf("--enclave is required (or set kurtosis.enclave_name in config.yaml)")
	}

	logLevel := reporting.LogLevelInfo
	if verbose {
		logLevel = reporting.LogLevelDebug
	}
	logger := reporting.NewLogger(reporting.LoggerConfig{
		Level:  logLevel,
		Format: reporting.LogFormat(appCfg.Framework.LogFormat),
		Output: os.Stdout,
	})

	fuzzCfg := &fuzz.Config{
		Enclave:      appCfg.Kurtosis.EnclaveName,
		Rounds:       rounds,
		CompoundBias: 0.7,
		CompoundOnly: compoundOnly,
		SingleOnly:   singleOnly,
		MaxFaults:    maxFaults,
		Trigger:      triggerName,
		Seed:         seed,
		DryRun:       dryRun,
		LogPath:      logPath,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := fuzz.NewRunner(fuzzCfg, appCfg, logger)
	return runner.Run(ctx)
}
