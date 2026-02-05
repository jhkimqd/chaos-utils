package main

import (
	"os"

	"github.com/spf13/cobra"
)

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
	Version: version,
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
		os.Exit(1)
	}
}
