package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jihwankim/chaos-utils/pkg/scenario/parser"
	"github.com/jihwankim/chaos-utils/pkg/scenario/validator"
	"github.com/spf13/cobra"
)

// validateResult is the JSON payload for `validate --format json`.
type validateResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

var validateCmd = &cobra.Command{
	Use:   "validate <path>",
	Args:  cobra.ExactArgs(1),
	Short: "Validate a chaos scenario YAML file",
	Long: `Parses and validates a scenario YAML using the same parser/validator the
'run' subcommand uses.

Exit codes:
  0  scenario is valid (warnings allowed)
  1  scenario parsed but failed validation
  2  file unreadable or YAML parse error`,
	RunE:          runValidate,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	validateCmd.Flags().String("format", "text", "output format (text, json)")
}

func runValidate(cmd *cobra.Command, args []string) error {
	path := args[0]
	format, _ := cmd.Flags().GetString("format")

	p := parser.New(nil)
	s, err := p.ParseFile(path)
	if err != nil {
		// Parse / read failure → exit 2 via InfraError.
		emitValidateError(format, err)
		return NewInfraError("%s", err)
	}

	v := validator.New()
	validateErr := v.Validate(s)

	if v.Errors == nil {
		v.Errors = []string{}
	}
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	result := validateResult{
		Valid:    validateErr == nil,
		Errors:   v.Errors,
		Warnings: v.Warnings,
	}

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
	} else {
		printValidateText(path, result)
	}

	if !result.Valid {
		// Validation failure → exit 1 (test/scenario finding, not infra).
		return fmt.Errorf("scenario validation failed with %d error(s)", len(result.Errors))
	}
	return nil
}

func emitValidateError(format string, err error) {
	if format == "json" {
		result := validateResult{
			Valid:    false,
			Errors:   []string{err.Error()},
			Warnings: []string{},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	fmt.Fprintf(os.Stderr, "parse error: %s\n", err)
}

func printValidateText(path string, r validateResult) {
	if r.Valid {
		fmt.Printf("OK %s\n", path)
	} else {
		fmt.Printf("FAIL %s\n", path)
	}
	if len(r.Errors) > 0 {
		fmt.Println("Errors:")
		for _, e := range r.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range r.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
}
