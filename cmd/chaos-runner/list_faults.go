package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

// registeredFaultTypes mirrors the validTypes slice in
// pkg/scenario/validator/validator.go::validateFaultType (line 272-284).
// Keep this in sync with that source — this is the user-facing whitelist.
var registeredFaultTypes = []string{
	"network",
	"cpu", "cpu_stress",
	"memory", "memory_stress", "memory_pressure",
	"container_restart", "container_kill", "container_pause",
	"connection_drop",
	"dns",
	"process_kill",
	"disk_io", "disk_fill", "file_delete", "file_corrupt",
	"clock_skew",
	"http_fault", "corruption_proxy", "p2p_attack",
	"disk", "process", "custom",
}

var listFaultsCmd = &cobra.Command{
	Use:   "list-faults",
	Args:  cobra.NoArgs,
	Short: "List registered fault types",
	Long: `Emits the registered fault-type whitelist. Source of truth:
pkg/scenario/validator/validator.go::validateFaultType.`,
	RunE: runListFaults,
}

func init() {
	listFaultsCmd.Flags().String("format", "text", "output format (text, json)")
}

func runListFaults(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")

	types := append([]string{}, registeredFaultTypes...)
	sort.Strings(types)

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(types)
	default:
		for _, t := range types {
			fmt.Println(t)
		}
		return nil
	}
}
