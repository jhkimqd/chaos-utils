package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// probeEntry describes a probe YAML doc shaped like a SuccessCriterion.
type probeEntry struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	ModeHint    string `json:"mode_hint,omitempty"`
	Error       string `json:"error,omitempty"`
}

// probeDoc is a minimal shape used to read probe files. It intentionally does
// not import pkg/scenario to keep this command lightweight; only fields used
// for listing are decoded.
type probeDoc struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	ModeHint    string `yaml:"mode_hint"`
}

var listProbesCmd = &cobra.Command{
	Use:   "list-probes",
	Args:  cobra.NoArgs,
	Short: "List reusable probes (success criteria) from probes/",
	Long: `Walks probes/ and lists each YAML doc shaped like a SuccessCriterion.
If probes/ does not exist, an empty list is emitted and the command exits 0.`,
	RunE: runListProbes,
}

func init() {
	listProbesCmd.Flags().String("format", "text", "output format (text, json)")
	listProbesCmd.Flags().String("dir", "probes", "directory to walk")
}

func runListProbes(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")
	dir, _ := cmd.Flags().GetString("dir")

	entries, err := collectProbes(dir)
	if err != nil {
		return NewInfraError("failed to walk %s: %w", dir, err)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	default:
		return printProbesText(entries)
	}
}

func collectProbes(dir string) ([]probeEntry, error) {
	entries := []probeEntry{}

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Missing root yields an empty list — sibling PR may not
			// have created probes/ yet.
			if path == dir && os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !isYAML(path) {
			return nil
		}

		entry := probeEntry{Path: path}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			entry.Error = readErr.Error()
			entries = append(entries, entry)
			return nil
		}
		var doc probeDoc
		if unmarshalErr := yaml.Unmarshal(data, &doc); unmarshalErr != nil {
			entry.Error = unmarshalErr.Error()
			entries = append(entries, entry)
			return nil
		}
		entry.Name = doc.Name
		entry.Description = doc.Description
		entry.Type = doc.Type
		entry.ModeHint = doc.ModeHint
		entries = append(entries, entry)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func printProbesText(entries []probeEntry) error {
	if len(entries) == 0 {
		fmt.Println("No probes found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tNAME\tTYPE\tMODE\tDESCRIPTION")
	for _, e := range entries {
		if e.Error != "" {
			fmt.Fprintf(w, "%s\t<parse error>\t-\t-\t%s\n", e.Path, e.Error)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Path, e.Name, e.Type, e.ModeHint, truncate(e.Description, 60))
	}
	return w.Flush()
}
