package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// faultParam documents a single fault parameter.
type faultParam struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Range   string `json:"range,omitempty"`
	Note    string `json:"note,omitempty"`
}

// faultExplanation documents a registered fault type.
type faultExplanation struct {
	Type    string       `json:"type"`
	Summary string       `json:"summary"`
	Params  []faultParam `json:"params"`
}

// faultCatalog is the v1 hand-curated catalog. Source of truth lives in
// pkg/scenario/validator/validator.go (validateFaultType) for the type list
// and pkg/scenario/types.go + pkg/injection/* for params. Update this map
// when those change.
var faultCatalog = map[string]faultExplanation{
	"network": {
		Type:    "network",
		Summary: "tc netem-based L3/L4 fault: latency, packet loss, bandwidth limits, port filtering.",
		Params: []faultParam{
			{Name: "device", Type: "string", Default: "eth0", Note: "Network interface inside the container."},
			{Name: "latency", Type: "int(ms)", Range: ">=0", Note: "Added delay per packet."},
			{Name: "packet_loss", Type: "float|int|\"NN%\"", Range: "0-100", Note: "Percent loss; accepts \"50%\"."},
			{Name: "bandwidth", Type: "int(kbit)", Range: ">=0", Note: "Egress rate cap."},
			{Name: "target_ports", Type: "string", Note: "Comma-separated port list to scope the fault."},
			{Name: "target_proto", Type: "string", Note: "Protocol filter (tcp|udp)."},
		},
	},
	"cpu_stress": {
		Type:    "cpu_stress",
		Summary: "stress-ng CPU load on the target container.",
		Params: []faultParam{
			{Name: "cpus", Type: "int", Default: "1", Note: "Number of CPU workers."},
			{Name: "load", Type: "int", Range: "1-100", Default: "100", Note: "Percent load per worker."},
			{Name: "duration", Type: "duration", Note: "Per-fault override; defaults to scenario duration."},
		},
	},
	"memory_stress": {
		Type:    "memory_stress",
		Summary: "stress-ng memory pressure on the target container.",
		Params: []faultParam{
			{Name: "size", Type: "string", Default: "512M", Note: "Memory to allocate (e.g. 256M, 1G)."},
			{Name: "workers", Type: "int", Default: "1", Note: "Number of memory workers."},
			{Name: "duration", Type: "duration", Note: "Per-fault override."},
		},
	},
	"container_kill": {
		Type:    "container_kill",
		Summary: "SIGKILL the target container via the Docker API.",
		Params: []faultParam{
			{Name: "signal", Type: "string", Default: "SIGKILL", Note: "Signal name."},
		},
	},
	"container_pause": {
		Type:    "container_pause",
		Summary: "Pause the target container; auto-unpauses at teardown.",
		Params: []faultParam{
			{Name: "duration", Type: "duration", Note: "Pause duration; defaults to scenario duration."},
		},
	},
	"container_restart": {
		Type:    "container_restart",
		Summary: "Restart the target container via the Docker API.",
		Params: []faultParam{
			{Name: "timeout", Type: "duration", Default: "10s", Note: "Graceful stop timeout before SIGKILL."},
		},
	},
	"connection_drop": {
		Type:    "connection_drop",
		Summary: "iptables-based connection reset against specific peers/ports.",
		Params: []faultParam{
			{Name: "target_ports", Type: "string", Note: "Ports to drop."},
			{Name: "target_proto", Type: "string", Default: "tcp", Note: "Protocol (tcp|udp)."},
			{Name: "direction", Type: "string", Default: "both", Note: "ingress|egress|both."},
		},
	},
	"dns": {
		Type:    "dns",
		Summary: "DNS failure injection (NXDOMAIN, SERVFAIL, latency).",
		Params: []faultParam{
			{Name: "mode", Type: "string", Default: "nxdomain", Note: "nxdomain|servfail|delay."},
			{Name: "domains", Type: "[]string", Note: "Specific domains to corrupt; empty = all."},
			{Name: "latency", Type: "int(ms)", Note: "Added DNS latency in delay mode."},
		},
	},
	"process_kill": {
		Type:    "process_kill",
		Summary: "Send a signal to a process inside the container.",
		Params: []faultParam{
			{Name: "process", Type: "string", Note: "Process name pattern."},
			{Name: "signal", Type: "string", Default: "SIGKILL", Note: "Signal to deliver."},
		},
	},
	"disk_io": {
		Type:    "disk_io",
		Summary: "Disk I/O stress (random reads/writes) via stress-ng.",
		Params: []faultParam{
			{Name: "workers", Type: "int", Default: "1"},
			{Name: "size", Type: "string", Default: "1G", Note: "Per-worker file size."},
		},
	},
	"disk_fill": {
		Type:    "disk_fill",
		Summary: "Fill disk to a target percentage.",
		Params: []faultParam{
			{Name: "fill_percent", Type: "int", Range: "1-99", Default: "90"},
			{Name: "path", Type: "string", Default: "/tmp", Note: "Mount path to fill."},
		},
	},
	"file_corrupt": {
		Type:    "file_corrupt",
		Summary: "Bit-flip corruption of a target file inside the container.",
		Params: []faultParam{
			{Name: "path", Type: "string", Note: "Absolute path of the file to corrupt."},
			{Name: "bytes", Type: "int", Default: "16", Note: "Number of random bytes to flip."},
		},
	},
	"file_delete": {
		Type:    "file_delete",
		Summary: "Delete a file inside the container.",
		Params: []faultParam{
			{Name: "path", Type: "string", Note: "Absolute path to delete."},
		},
	},
	"clock_skew": {
		Type:    "clock_skew",
		Summary: "Manipulate the container clock (libfaketime).",
		Params: []faultParam{
			{Name: "skew", Type: "duration", Note: "Offset (e.g. +30s, -2m)."},
		},
	},
	"http_fault": {
		Type:    "http_fault",
		Summary: "Envoy L7 fault: abort, delay, header/body override.",
		Params: []faultParam{
			{Name: "mode", Type: "string", Note: "abort|delay|override."},
			{Name: "abort_status", Type: "int", Note: "HTTP status when mode=abort."},
			{Name: "delay", Type: "duration", Note: "Added latency when mode=delay."},
			{Name: "match", Type: "object", Note: "Path/header matchers."},
		},
	},
	"corruption_proxy": {
		Type:    "corruption_proxy",
		Summary: "JSON-aware semantic corruption proxy for Bor RPC / Heimdall REST. See scenarios/polygon-chain/semantic/rules/_REFERENCE.yaml.",
		Params: []faultParam{
			{Name: "rules_file", Type: "string", Note: "Path to corruption rules YAML."},
			{Name: "upstream_port", Type: "int", Note: "Port the proxy forwards to."},
			{Name: "listen_port", Type: "int", Note: "Port the proxy listens on."},
		},
	},
	"p2p_attack": {
		Type:    "p2p_attack",
		Summary: "chaos-peer devp2p attacks against Bor (RLPx-level).",
		Params: []faultParam{
			{Name: "attack", Type: "string", Note: "eclipse|fork-flood|invalid-block|...; see pkg/injection/p2p/bor."},
			{Name: "duration", Type: "duration", Note: "Per-fault override."},
		},
	},
}

var explainCmd = &cobra.Command{
	Use:   "explain <fault-type>",
	Args:  cobra.ExactArgs(1),
	Short: "Describe accepted parameters for a registered fault type",
	Long: `Prints the documented parameter schema for a fault type. The catalog is
hand-curated against pkg/scenario/validator/validator.go and the relevant
pkg/injection/* handlers.`,
	RunE: runExplain,
}

func init() {
	explainCmd.Flags().String("format", "text", "output format (text, json)")
}

func runExplain(cmd *cobra.Command, args []string) error {
	faultType := args[0]
	format, _ := cmd.Flags().GetString("format")

	entry, ok := faultCatalog[faultType]
	if !ok {
		return fmt.Errorf("fault type %q is not documented; run 'chaos-runner list-faults' for the registered set", faultType)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entry)
	default:
		printExplainText(entry)
		return nil
	}
}

func printExplainText(e faultExplanation) {
	fmt.Printf("Fault type: %s\n", e.Type)
	fmt.Printf("Summary:    %s\n\n", e.Summary)
	if len(e.Params) == 0 {
		fmt.Println("(no documented parameters)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PARAM\tTYPE\tDEFAULT\tRANGE\tNOTE")
	for _, p := range e.Params {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Type, p.Default, p.Range, p.Note)
	}
	_ = w.Flush()
}
