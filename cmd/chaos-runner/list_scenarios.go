package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jihwankim/chaos-utils/pkg/scenario/parser"
	"github.com/spf13/cobra"
)

// scenarioEntry is the per-file payload emitted by `list-scenarios`.
type scenarioEntry struct {
	Path           string   `json:"path"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Tags           []string `json:"tags"`
	Version        string   `json:"version"`
	FaultCount     int      `json:"fault_count"`
	CriterionCount int      `json:"criterion_count"`
	Error          string   `json:"error,omitempty"`
}

var listScenariosCmd = &cobra.Command{
	Use:   "list-scenarios",
	Args:  cobra.NoArgs,
	Short: "List all built-in chaos scenarios",
	Long: `Walks scenarios/ recursively and reports metadata for every YAML
scenario found. Files that fail to parse are reported with the error and do
not abort the walk.`,
	RunE: runListScenarios,
}

func init() {
	listScenariosCmd.Flags().String("format", "text", "output format (text, json)")
	listScenariosCmd.Flags().String("dir", "scenarios", "directory to walk")
}

func runListScenarios(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")
	dir, _ := cmd.Flags().GetString("dir")

	entries, err := collectScenarios(dir)
	if err != nil {
		return NewInfraError("failed to walk %s: %w", dir, err)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	default:
		return printScenariosText(entries)
	}
}

func collectScenarios(dir string) ([]scenarioEntry, error) {
	entries := []scenarioEntry{}

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Missing root yields an empty list — keeps the command
			// useful in worktrees that don't ship the scenarios tree.
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

		entry := scenarioEntry{Path: path, Tags: []string{}}
		p := parser.New(nil)
		s, parseErr := p.ParseFile(path)
		if parseErr != nil {
			entry.Error = parseErr.Error()
			entries = append(entries, entry)
			return nil
		}

		entry.Name = s.Metadata.Name
		entry.Description = s.Metadata.Description
		if s.Metadata.Tags != nil {
			entry.Tags = s.Metadata.Tags
		}
		entry.Version = s.Metadata.Version
		entry.FaultCount = len(s.Spec.Faults)
		entry.CriterionCount = len(s.Spec.SuccessCriteria)
		entries = append(entries, entry)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func printScenariosText(entries []scenarioEntry) error {
	if len(entries) == 0 {
		fmt.Println("No scenarios found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tNAME\tFAULTS\tCRITERIA\tDESCRIPTION")
	for _, e := range entries {
		if e.Error != "" {
			fmt.Fprintf(w, "%s\t<parse error>\t-\t-\t%s\n", e.Path, e.Error)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", e.Path, e.Name, e.FaultCount, e.CriterionCount, truncate(e.Description, 60))
	}
	return w.Flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
