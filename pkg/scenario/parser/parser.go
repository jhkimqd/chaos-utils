package parser

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// Parser parses chaos scenario YAML files
type Parser struct {
	// Variables for substitution
	Variables map[string]string
}

// New creates a new parser with optional variables
func New(variables map[string]string) *Parser {
	if variables == nil {
		variables = make(map[string]string)
	}
	return &Parser{
		Variables: variables,
	}
}

// ParseFile parses a scenario from a YAML file
func (p *Parser) ParseFile(path string) (*scenario.Scenario, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario file: %w", err)
	}

	return p.Parse(data)
}

// Parse parses a scenario from YAML bytes
func (p *Parser) Parse(data []byte) (*scenario.Scenario, error) {
	// Apply variable substitution
	substituted := p.substituteVariables(string(data))

	// Parse YAML
	var s scenario.Scenario
	if err := yaml.Unmarshal([]byte(substituted), &s); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate required fields
	if err := p.validateRequiredFields(&s); err != nil {
		return nil, err
	}

	return &s, nil
}

// substituteVariables replaces ${VAR} and $VAR with values from environment and parser variables
func (p *Parser) substituteVariables(content string) string {
	// Pattern matches ${VAR} and $VAR
	re := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

	result := re.ReplaceAllStringFunc(content, func(match string) string {
		// Extract variable name
		var varName string
		if strings.HasPrefix(match, "${") {
			varName = match[2 : len(match)-1] // Remove ${ and }
		} else {
			varName = match[1:] // Remove $
		}

		// Check parser variables first
		if val, ok := p.Variables[varName]; ok {
			return val
		}

		// Check environment variables
		if val := os.Getenv(varName); val != "" {
			return val
		}

		// Return original if not found
		return match
	})

	return result
}

// SetVariable sets a variable for substitution
func (p *Parser) SetVariable(key, value string) {
	p.Variables[key] = value
}

// SetVariables sets multiple variables
func (p *Parser) SetVariables(vars map[string]string) {
	for k, v := range vars {
		p.Variables[k] = v
	}
}

// ParseOverrides parses CLI override strings (--set key=value)
// Supports dotted paths like "spec.duration=10m"
func ParseOverrides(overrides []string) (map[string]string, error) {
	result := make(map[string]string)

	for _, override := range overrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid override format: %s (expected key=value)", override)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("empty key in override: %s", override)
		}

		result[key] = value
	}

	return result, nil
}

// ApplyOverrides applies CLI overrides to a scenario
// This is a simple implementation that handles common cases
func ApplyOverrides(s *scenario.Scenario, overrides map[string]string) error {
	for key, value := range overrides {
		switch key {
		case "duration", "spec.duration":
			duration, err := parseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid duration override: %w", err)
			}
			s.Spec.Duration = duration

		case "warmup", "spec.warmup":
			duration, err := parseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid warmup override: %w", err)
			}
			s.Spec.Warmup = duration

		case "cooldown", "spec.cooldown":
			duration, err := parseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid cooldown override: %w", err)
			}
			s.Spec.Cooldown = duration

		case "enclave", "spec.targets[0].selector.enclave":
			if len(s.Spec.Targets) > 0 {
				s.Spec.Targets[0].Selector.Enclave = value
			}

		default:
			return fmt.Errorf("unsupported override key: %s", key)
		}
	}

	return nil
}

// parseDuration is a helper to parse duration strings
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format: %s (use format like 5m, 1h, 30s)", s)
	}
	return d, nil
}

// validateRequiredFields validates that required fields are present
func (p *Parser) validateRequiredFields(s *scenario.Scenario) error {
	if s.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}

	if s.Kind == "" {
		return fmt.Errorf("kind is required")
	}

	if s.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}

	if len(s.Spec.Targets) == 0 {
		return fmt.Errorf("spec.targets is required and must have at least one target")
	}

	if s.Spec.Duration == 0 {
		return fmt.Errorf("spec.duration is required")
	}

	if len(s.Spec.Faults) == 0 {
		return fmt.Errorf("spec.faults is required and must have at least one fault")
	}

	// Validate each target
	for i, target := range s.Spec.Targets {
		if target.Alias == "" {
			return fmt.Errorf("spec.targets[%d].alias is required", i)
		}
		if target.Selector.Type == "" {
			return fmt.Errorf("spec.targets[%d].selector.type is required", i)
		}
	}

	// Validate each fault
	for i, fault := range s.Spec.Faults {
		if fault.Target == "" {
			return fmt.Errorf("spec.faults[%d].target is required", i)
		}
		if fault.Type == "" {
			return fmt.Errorf("spec.faults[%d].type is required", i)
		}
		if len(fault.Params) == 0 {
			return fmt.Errorf("spec.faults[%d].params is required", i)
		}
	}

	return nil
}
