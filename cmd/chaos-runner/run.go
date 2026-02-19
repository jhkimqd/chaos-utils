package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/config"
	"github.com/jihwankim/chaos-utils/pkg/core/orchestrator"
	"github.com/jihwankim/chaos-utils/pkg/reporting"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
	"github.com/jihwankim/chaos-utils/pkg/scenario/parser"
	"github.com/jihwankim/chaos-utils/pkg/scenario/validator"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Args:  cobra.NoArgs,
	Short: "Execute a chaos test scenario",
	Long:  `Loads a scenario YAML file and executes the chaos test.`,
	RunE:  runChaosTest,
}

func init() {
	runCmd.Flags().String("scenario", "", "path to scenario YAML file")
	runCmd.Flags().StringArray("set", []string{}, "override scenario values (e.g., --set duration=10m)")
	runCmd.Flags().String("enclave", "", "Kurtosis enclave name (overrides config)")
	runCmd.Flags().String("format", "text", "output format (text, json, tui)")
	runCmd.Flags().Bool("dry-run", false, "validate scenario without executing")
}

func runChaosTest(cmd *cobra.Command, args []string) error {
	// Get flags
	scenarioPath, _ := cmd.Flags().GetString("scenario")
	if scenarioPath == "" {
		return fmt.Errorf("--scenario flag is required")
	}
	setFlags, _ := cmd.Flags().GetStringArray("set")
	enclaveName, _ := cmd.Flags().GetString("enclave")
	outputFormat, _ := cmd.Flags().GetString("format")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Override enclave if specified
	if enclaveName != "" {
		cfg.Kurtosis.EnclaveName = enclaveName
	}

	// Auto-discover Prometheus if not explicitly configured via env var
	if os.Getenv("PROMETHEUS_URL") == "" {
		fmt.Println("⚙️  Prometheus URL not configured, attempting auto-discovery from Kurtosis...")
		if endpoint, err := config.DiscoverPrometheusEndpoint(cfg.Kurtosis.EnclaveName); err == nil {
			cfg.Prometheus.URL = endpoint
			fmt.Printf("✓ Discovered Prometheus endpoint: %s\n", endpoint)
		} else {
			fmt.Printf("⚠️  Auto-discovery failed: %v\n", err)
			fmt.Printf("   Using: %s\n", cfg.Prometheus.URL)
		}
	}

	// Initialize logger
	logLevel := reporting.LogLevelInfo
	if verbose {
		logLevel = reporting.LogLevelDebug
	}
	logFormat := reporting.LogFormat(cfg.Framework.LogFormat)

	logger := reporting.NewLogger(reporting.LoggerConfig{
		Level:  logLevel,
		Format: logFormat,
		Output: os.Stdout,
	})

	logger.Info("Chaos Runner starting", "version", version)

	// Parse scenario
	logger.Info("Parsing scenario", "file", scenarioPath)
	p := parser.New(nil)
	scenario, err := p.ParseFile(scenarioPath)
	if err != nil {
		return fmt.Errorf("failed to parse scenario: %w", err)
	}

	// Apply overrides
	if len(setFlags) > 0 {
		overrides := parseSetFlags(setFlags)
		if err := parser.ApplyOverrides(scenario, overrides); err != nil {
			return fmt.Errorf("failed to apply overrides: %w", err)
		}
		logger.Debug("Applied overrides", "count", len(overrides))
	}

	// Validate scenario
	logger.Info("Validating scenario")
	v := validator.New()
	if err := v.Validate(scenario); err != nil {
		return fmt.Errorf("scenario validation failed: %w", err)
	}

	if len(v.Warnings) > 0 {
		logger.Warn("Scenario has warnings")
		for _, warning := range v.Warnings {
			logger.Warn("  " + warning)
		}
	}

	logger.Info("Scenario validated successfully", "name", scenario.Metadata.Name)

	// Dry run - exit after validation
	if dryRun {
		fmt.Println("✅ Scenario is valid (dry-run mode)")
		return nil
	}

	// Create orchestrator
	logger.Info("Creating orchestrator")
	orch, err := orchestrator.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Create progress reporter
	progressReporter := reporting.NewProgressReporter(
		reporting.OutputFormat(outputFormat),
		logger,
	)

	// Create storage for results
	storage, err := reporting.NewStorage(cfg.Reporting.OutputDir, cfg.Reporting.KeepLastN, logger)
	if err != nil {
		return fmt.Errorf("failed to create storage: %w", err)
	}

	// Execute test
	ctx := context.Background()
	logger.Info("Starting chaos test execution", "scenario", scenario.Metadata.Name)

	result, err := orch.Execute(ctx, scenarioPath)

	// Generate report regardless of success/failure
	report := &reporting.TestReport{
		TestID:         result.TestID,
		ScenarioName:   scenario.Metadata.Name,
		StartTime:      result.StartTime,
		EndTime:        result.EndTime,
		Duration:       result.Duration.String(),
		Status:         convertStatus(result.State),
		Success:        result.Success,
		Message:        result.Message,
		Targets:        convertTargets(result.Targets),
		Faults:         convertFaults(scenario, result),
		CleanupSummary: orch.GetCleanupSummary(),
		Errors:         convertErrors(result.Errors),
	}

	// Save report
	if _, saveErr := storage.SaveReport(report); saveErr != nil {
		logger.Warn("Failed to save report", "error", saveErr)
	}

	// Display final summary
	progressReporter.ReportTestCompleted(report)

	// Return error if test failed
	if err != nil {
		return fmt.Errorf("chaos test failed: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("chaos test did not meet success criteria")
	}

	logger.Info("Chaos test completed successfully")
	return nil
}

// parseSetFlags parses --set flags into a map
func parseSetFlags(setFlags []string) map[string]string {
	overrides := make(map[string]string)
	for _, flag := range setFlags {
		parts := strings.SplitN(flag, "=", 2)
		if len(parts) == 2 {
			overrides[parts[0]] = parts[1]
		}
	}
	return overrides
}

// convertStatus converts orchestrator state to report status
func convertStatus(state orchestrator.TestState) reporting.TestStatus {
	switch state {
	case orchestrator.StateCompleted:
		return reporting.StatusCompleted
	case orchestrator.StateFailed:
		return reporting.StatusFailed
	default:
		return reporting.StatusRunning
	}
}

// convertErrors converts error slice to string slice
func convertErrors(errs []error) []string {
	result := make([]string, len(errs))
	for i, err := range errs {
		result[i] = err.Error()
	}
	return result
}

// convertTargets converts orchestrator.TargetInfo to reporting.TargetInfo
func convertTargets(targets []orchestrator.TargetInfo) []reporting.TargetInfo {
	result := make([]reporting.TargetInfo, len(targets))
	for i, t := range targets {
		result[i] = reporting.TargetInfo{
			Alias:       t.Alias,
			ServiceName: t.Name,
			ContainerID: t.ContainerID,
			IP:          t.IP,
		}
	}
	return result
}

// convertFaults converts scenario faults to reporting.FaultInfo
func convertFaults(s *scenario.Scenario, result *orchestrator.TestResult) []reporting.FaultInfo {
	if s == nil || len(s.Spec.Faults) == 0 {
		return nil
	}

	faults := make([]reporting.FaultInfo, 0, len(s.Spec.Faults))
	for _, f := range s.Spec.Faults {
		faultInfo := reporting.FaultInfo{
			Phase:       f.Phase,
			Type:        f.Type,
			Target:      f.Target,
			Description: f.Description,
			StartTime:   result.StartTime,
			EndTime:     result.EndTime,
			Duration:    result.Duration.String(),
			Parameters:  make(map[string]interface{}),
		}

		// Convert fault parameters to map
		if f.Params != nil {
			for k, v := range f.Params {
				faultInfo.Parameters[k] = v
			}
		}

		faults = append(faults, faultInfo)
	}

	return faults
}
