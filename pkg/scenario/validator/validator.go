package validator

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// Validator validates chaos scenarios
type Validator struct {
	// Warnings are non-fatal issues
	Warnings []string

	// Errors are fatal issues
	Errors []string
}

// New creates a new validator
func New() *Validator {
	return &Validator{
		Warnings: make([]string, 0),
		Errors:   make([]string, 0),
	}
}

// Validate validates a scenario
func (v *Validator) Validate(s *scenario.Scenario) error {
	v.Warnings = make([]string, 0)
	v.Errors = make([]string, 0)

	// Validate API version and kind
	v.validateAPIVersion(s)
	v.validateKind(s)

	// Validate metadata
	v.validateMetadata(s)

	// Validate spec
	v.validateSpec(s)

	// Validate targets
	v.validateTargets(s)

	// Validate faults
	v.validateFaults(s)

	// Validate success criteria
	v.validateSuccessCriteria(s)

	// Check for dangerous scenarios
	v.checkDangerousScenarios(s)

	if len(v.Errors) > 0 {
		return fmt.Errorf("validation failed with %d errors", len(v.Errors))
	}

	return nil
}

// HasWarnings returns true if there are warnings
func (v *Validator) HasWarnings() bool {
	return len(v.Warnings) > 0
}

// HasErrors returns true if there are errors
func (v *Validator) HasErrors() bool {
	return len(v.Errors) > 0
}

// GetReport returns a formatted validation report
func (v *Validator) GetReport() string {
	var sb strings.Builder

	if len(v.Errors) > 0 {
		sb.WriteString("ERRORS:\n")
		for _, err := range v.Errors {
			sb.WriteString(fmt.Sprintf("  - %s\n", err))
		}
	}

	if len(v.Warnings) > 0 {
		sb.WriteString("\nWARNINGS:\n")
		for _, warn := range v.Warnings {
			sb.WriteString(fmt.Sprintf("  - %s\n", warn))
		}
	}

	if len(v.Errors) == 0 && len(v.Warnings) == 0 {
		sb.WriteString("Validation passed with no issues.\n")
	}

	return sb.String()
}

func (v *Validator) validateAPIVersion(s *scenario.Scenario) {
	if s.APIVersion == "" {
		v.Errors = append(v.Errors, "apiVersion is required")
		return
	}

	// Check for supported API versions
	supportedVersions := []string{"chaos.polygon.io/v1", "chaos/v1"}
	supported := false
	for _, ver := range supportedVersions {
		if s.APIVersion == ver {
			supported = true
			break
		}
	}

	if !supported {
		v.Warnings = append(v.Warnings, fmt.Sprintf("apiVersion '%s' may not be supported (expected: chaos.polygon.io/v1)", s.APIVersion))
	}
}

func (v *Validator) validateKind(s *scenario.Scenario) {
	if s.Kind == "" {
		v.Errors = append(v.Errors, "kind is required")
		return
	}

	if s.Kind != "ChaosScenario" {
		v.Warnings = append(v.Warnings, fmt.Sprintf("kind '%s' may not be supported (expected: ChaosScenario)", s.Kind))
	}
}

func (v *Validator) validateMetadata(s *scenario.Scenario) {
	if s.Metadata.Name == "" {
		v.Errors = append(v.Errors, "metadata.name is required")
	}

	// Check for valid name format (DNS-like)
	if s.Metadata.Name != "" {
		nameRegex := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
		if !nameRegex.MatchString(s.Metadata.Name) {
			v.Errors = append(v.Errors, "metadata.name must be lowercase alphanumeric with hyphens")
		}
	}
}

func (v *Validator) validateSpec(s *scenario.Scenario) {
	if s.Spec.Duration == 0 {
		v.Errors = append(v.Errors, "spec.duration is required and must be > 0")
	}

	if s.Spec.Duration < 0 {
		v.Errors = append(v.Errors, "spec.duration cannot be negative")
	}

	// Check for excessively long duration
	if s.Spec.Duration.Hours() > 24 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("spec.duration is very long (%.1f hours)", s.Spec.Duration.Hours()))
	}

	// Validate execution mode
	if s.Spec.ExecutionMode != "" {
		validModes := []string{"sequential", "parallel"}
		valid := false
		for _, mode := range validModes {
			if s.Spec.ExecutionMode == mode {
				valid = true
				break
			}
		}
		if !valid {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.execution_mode '%s' is invalid (must be 'sequential' or 'parallel')", s.Spec.ExecutionMode))
		}
	}
}

func (v *Validator) validateTargets(s *scenario.Scenario) {
	if len(s.Spec.Targets) == 0 {
		v.Errors = append(v.Errors, "spec.targets must have at least one target")
		return
	}

	targetAliases := make(map[string]bool)

	for i, target := range s.Spec.Targets {
		// Check alias
		if target.Alias == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].alias is required", i))
		} else {
			// Check for duplicate aliases
			if targetAliases[target.Alias] {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].alias '%s' is duplicated", i, target.Alias))
			}
			targetAliases[target.Alias] = true
		}

		// Validate selector
		v.validateTargetSelector(target.Selector, i)
	}
}

func (v *Validator) validateTargetSelector(sel scenario.TargetSelector, index int) {
	if sel.Type == "" {
		v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].selector.type is required", index))
		return
	}

	validTypes := []string{"kurtosis_service", "docker_container"}
	valid := false
	for _, t := range validTypes {
		if sel.Type == t {
			valid = true
			break
		}
	}
	if !valid {
		v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].selector.type '%s' is invalid", index, sel.Type))
	}

	// Type-specific validation
	switch sel.Type {
	case "kurtosis_service":
		if sel.Pattern == "" && sel.ServiceName == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].selector must have pattern or service_name for kurtosis_service type", index))
		}
		if sel.Pattern != "" {
			if _, err := regexp.Compile(sel.Pattern); err != nil {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].selector.pattern is invalid regex: %v", index, err))
			}
		}

	case "docker_container":
		if sel.Pattern == "" && sel.ContainerID == "" && len(sel.Labels) == 0 {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.targets[%d].selector must have pattern, container_id, or labels for docker_container type", index))
		}
	}
}

func (v *Validator) validateFaults(s *scenario.Scenario) {
	if len(s.Spec.Faults) == 0 {
		v.Errors = append(v.Errors, "spec.faults must have at least one fault")
		return
	}

	// Build set of valid target aliases
	validTargets := make(map[string]bool)
	for _, target := range s.Spec.Targets {
		validTargets[target.Alias] = true
	}

	for i, fault := range s.Spec.Faults {
		// Validate target reference
		if fault.Target == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].target is required", i))
		} else if !validTargets[fault.Target] {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].target '%s' references non-existent target alias", i, fault.Target))
		}

		// Validate fault type
		if fault.Type == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].type is required", i))
		} else {
			v.validateFaultType(fault, i)
		}

		// Validate params
		if len(fault.Params) == 0 {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params is required", i))
		} else {
			v.validateFaultParams(fault, i)
		}
	}
}

func (v *Validator) validateFaultType(fault scenario.Fault, index int) {
	validTypes := []string{
		"network",
		"cpu", "cpu_stress",
		"memory", "memory_stress", "memory_pressure",
		"container_restart", "container_kill", "container_pause",
		"disk", "process", "custom",
	}
	valid := false
	for _, t := range validTypes {
		if fault.Type == t {
			valid = true
			break
		}
	}

	if !valid {
		v.Warnings = append(v.Warnings, fmt.Sprintf("spec.faults[%d].type '%s' may not be supported", index, fault.Type))
	}
}

func (v *Validator) validateFaultParams(fault scenario.Fault, index int) {
	switch fault.Type {
	case "network":
		v.validateNetworkFaultParams(fault.Params, index)
	// Add more fault type validations as needed
	}
}

func (v *Validator) validateNetworkFaultParams(params map[string]interface{}, index int) {
	nfp := scenario.ParseNetworkParams(params)

	// Validate packet loss
	if nfp.PacketLoss < 0 || nfp.PacketLoss > 100 {
		v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params.packet_loss must be between 0 and 100", index))
	}

	// Validate latency
	if nfp.Latency < 0 {
		v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params.latency cannot be negative", index))
	}

	// Validate bandwidth
	if nfp.Bandwidth < 0 {
		v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params.bandwidth cannot be negative", index))
	}

	// Validate CIDR if present
	if nfp.TargetCIDR != "" {
		if _, _, err := net.ParseCIDR(nfp.TargetCIDR); err != nil {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params.target_cidr is invalid: %v", index, err))
		}
	}

	// Validate IPs if present
	if nfp.TargetIPs != "" {
		ips := strings.Split(nfp.TargetIPs, ",")
		for _, ip := range ips {
			if net.ParseIP(strings.TrimSpace(ip)) == nil {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.faults[%d].params.target_ips contains invalid IP: %s", index, ip))
			}
		}
	}
}

func (v *Validator) validateSuccessCriteria(s *scenario.Scenario) {
	for i, criterion := range s.Spec.SuccessCriteria {
		if criterion.Name == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.success_criteria[%d].name is required", i))
		}

		if criterion.Type == "" {
			v.Errors = append(v.Errors, fmt.Sprintf("spec.success_criteria[%d].type is required", i))
		}

		// Type-specific validation
		switch criterion.Type {
		case "prometheus":
			if criterion.Query == "" {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.success_criteria[%d].query is required for prometheus type", i))
			}
			if criterion.Threshold == "" {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.success_criteria[%d].threshold is required for prometheus type", i))
			}

		case "health_check":
			if criterion.URL == "" {
				v.Errors = append(v.Errors, fmt.Sprintf("spec.success_criteria[%d].url is required for health_check type", i))
			}
		}
	}
}

func (v *Validator) checkDangerousScenarios(s *scenario.Scenario) {
	// Check for 100% packet loss to all services
	allTargetsPattern := false
	for _, target := range s.Spec.Targets {
		if target.Selector.Pattern == ".*" || target.Selector.Pattern == ".+" {
			allTargetsPattern = true
			break
		}
	}

	if allTargetsPattern {
		for _, fault := range s.Spec.Faults {
			if fault.Type == "network" {
				nfp := scenario.ParseNetworkParams(fault.Params)
				if nfp.PacketLoss == 100 && nfp.TargetPorts == "" {
					v.Warnings = append(v.Warnings, "DANGEROUS: 100% packet loss to all services without port filtering may cause complete network isolation")
				}
			}
		}
	}

	// Check for very long durations
	if s.Spec.Duration.Hours() > 1 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("Long test duration (%.1f hours) - ensure this is intentional", s.Spec.Duration.Hours()))
	}

	// Check for missing success criteria
	if len(s.Spec.SuccessCriteria) == 0 {
		v.Warnings = append(v.Warnings, "No success criteria defined - test results will be harder to interpret")
	}
}
