package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// InfraError wraps infrastructure errors that should exit with code 2
// (distinct from test criteria failures which exit with code 1).
// The CI uses this distinction: exit 1 = test failure, exit 2+ = infra error.
type InfraError struct {
	Err error
}

func (e *InfraError) Error() string { return e.Err.Error() }
func (e *InfraError) Unwrap() error { return e.Err }

// NewInfraError creates an infrastructure error that will cause exit code 2.
func NewInfraError(format string, a ...interface{}) *InfraError {
	return &InfraError{Err: fmt.Errorf(format, a...)}
}

var (
	// Global flags
	cfgFile string
	verbose bool
	version = "dev" // Will be set by build flags
)

var rootCmd = &cobra.Command{
	Use:   "chaos-runner",
	Short: "Chaos engineering framework for Polygon PoS networks",
	Long: `Chaos Runner is a comprehensive chaos engineering platform for Kurtosis-managed
Polygon PoS networks. It provides declarative scenario definitions, automated
orchestration, integrated observability, and emergency stop mechanisms.`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
}

// Commands are defined in separate files:
// - runCmd in run.go

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		// Exit code 2 for infrastructure errors (config, connectivity, setup failures).
		// Exit code 1 for test criteria failures (scenario ran but didn't meet thresholds).
		// The CI workflow uses this distinction to separate infra breakage from expected test findings.
		var infraErr *InfraError
		if errors.As(err, &infraErr) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
