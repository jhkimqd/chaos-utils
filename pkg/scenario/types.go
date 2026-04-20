package scenario

import (
	"fmt"
	"time"
)

// Scenario represents a complete chaos test scenario
type Scenario struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   Metadata       `yaml:"metadata"`
	Spec       ScenarioSpec   `yaml:"spec"`
}

// Metadata contains scenario metadata
type Metadata struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	Author      string   `yaml:"author,omitempty"`
	Version     string   `yaml:"version,omitempty"`
}

// ScenarioSpec defines the scenario specification
type ScenarioSpec struct {
	// Targets define which services to target
	Targets []Target `yaml:"targets"`

	// Duration is the total test duration
	Duration time.Duration `yaml:"duration"`

	// Warmup period before injecting faults
	Warmup time.Duration `yaml:"warmup,omitempty"`

	// Cooldown period after removing faults
	Cooldown time.Duration `yaml:"cooldown,omitempty"`

	// Faults to inject
	Faults []Fault `yaml:"faults"`

	// SuccessCriteria defines what success looks like
	SuccessCriteria []SuccessCriterion `yaml:"success_criteria,omitempty"`

	// Metrics to collect during the test
	Metrics []string `yaml:"metrics,omitempty"`

	// Execution mode: sequential or parallel
	ExecutionMode string `yaml:"execution_mode,omitempty"`

	// Preconditions are topology requirements that must hold for the scenario
	// to be meaningful. Checked after target discovery; the scenario is
	// skipped with a clear error if unmet, instead of silently targeting a
	// devnet too small to exercise the intended fault.
	Preconditions *Preconditions `yaml:"preconditions,omitempty"`
}

// Preconditions encodes topology requirements for a scenario. A scenario that
// targets e.g. validator index 5 cannot run meaningfully on a 4-validator
// devnet; declare MinValidators so the runner fails fast with a helpful
// message rather than producing confusing "no containers matched" errors
// downstream.
type Preconditions struct {
	// MinValidators requires at least N containers to match ValidatorPattern.
	// Zero disables the check.
	MinValidators int `yaml:"min_validators,omitempty"`

	// ValidatorPattern is the regex used to count validators. If empty, the
	// orchestrator uses its default pattern (Polygon PoS Kurtosis convention:
	// "l2-cl-[0-9]+-heimdall-v2-bor-validator").
	ValidatorPattern string `yaml:"validator_pattern,omitempty"`
}

// Target defines a service or group of services to target
type Target struct {
	// Selector for finding services
	Selector TargetSelector `yaml:"selector"`

	// Alias for referencing this target in faults
	Alias string `yaml:"alias"`

	// Count limits the number of matching services (0 = all)
	Count int `yaml:"count,omitempty"`
}

// TargetSelector defines how to select target services
type TargetSelector struct {
	// Type of selector: kurtosis_service, docker_container
	Type string `yaml:"type"`

	// Enclave name for Kurtosis services
	Enclave string `yaml:"enclave,omitempty"`

	// Pattern is a regex pattern for service/container name matching
	Pattern string `yaml:"pattern,omitempty"`

	// Labels for Docker label-based selection
	Labels map[string]string `yaml:"labels,omitempty"`

	// ContainerID for direct container targeting
	ContainerID string `yaml:"container_id,omitempty"`

	// ServiceName for direct service name targeting
	ServiceName string `yaml:"service_name,omitempty"`
}

// Fault defines a chaos fault to inject
type Fault struct {
	// Phase name for organizing faults
	Phase string `yaml:"phase,omitempty"`

	// Description of what this fault does
	Description string `yaml:"description,omitempty"`

	// Target alias (from Targets section)
	Target string `yaml:"target"`

	// Type of fault: network, cpu, memory, disk, process, custom
	Type string `yaml:"type"`

	// Params are fault-specific parameters
	Params map[string]interface{} `yaml:"params"`

	// Duration for this specific fault (overrides scenario duration)
	Duration time.Duration `yaml:"duration,omitempty"`

	// Delay before injecting this fault
	Delay time.Duration `yaml:"delay,omitempty"`

	// ExcludeProducer dynamically excludes the current block producer from targets
	ExcludeProducer bool `yaml:"exclude_producer,omitempty"`
}

// SuccessCriterion defines a success criterion for the test
type SuccessCriterion struct {
	// Name of the criterion
	Name string `yaml:"name"`

	// Description of what this checks
	Description string `yaml:"description,omitempty"`

	// Type: prometheus, log, state_root_consensus
	Type string `yaml:"type"`

	// Query for Prometheus-based criteria
	Query string `yaml:"query,omitempty"`

	// Threshold to compare against (e.g., "> 0", "< 100", "== 0")
	Threshold string `yaml:"threshold,omitempty"`

	// Window is the time window for evaluation
	Window time.Duration `yaml:"window,omitempty"`

	// Critical marks this as a critical criterion (test fails if this fails)
	Critical bool `yaml:"critical,omitempty"`

	// PostFaultOnly skips this criterion during the pre-fault health check.
	// Use for criteria that verify fault effectiveness (e.g., "partitioned
	// validator stops advancing") — these are expected to fail before injection.
	PostFaultOnly bool `yaml:"post_fault_only,omitempty"`

	// DuringFault evaluates this criterion while faults are active (end of
	// MONITOR phase) instead of after teardown (DETECT phase). Use for
	// criteria that are only meaningful while the fault is injected, such as
	// verifying a partitioned validator has stalled — after faults are
	// removed the node recovers and the check becomes meaningless.
	DuringFault bool `yaml:"during_fault,omitempty"`

	// --- Log-based criteria fields (type: "log") ---

	// Pattern is a regex pattern to search for in container logs.
	Pattern string `yaml:"pattern,omitempty"`

	// TargetLog specifies which target alias's containers to scan.
	// If empty, all scenario targets are scanned.
	TargetLog string `yaml:"target_log,omitempty"`

	// ContainerPattern is a glob/substring pattern for discovering containers
	// to scan by name (e.g., "heimdall-v2-bor-validator"). This allows
	// scanning containers that are NOT scenario targets. If set, TargetLog
	// is ignored and containers are discovered via Docker API.
	ContainerPattern string `yaml:"container_pattern,omitempty"`

	// Absence inverts the check: pass if the pattern is NOT found.
	// Default false = pass if pattern IS found.
	Absence bool `yaml:"absence,omitempty"`
}

// NetworkFaultParams defines parameters for network faults
type NetworkFaultParams struct {
	Device      string  `yaml:"device,omitempty"`
	Latency     int     `yaml:"latency,omitempty"`
	PacketLoss  float64 `yaml:"packet_loss,omitempty"`
	Bandwidth   int     `yaml:"bandwidth,omitempty"`
	TargetPorts string  `yaml:"target_ports,omitempty"`
	TargetProto string  `yaml:"target_proto,omitempty"`
}

// ParseNetworkParams converts generic params to NetworkFaultParams
func ParseNetworkParams(params map[string]interface{}) NetworkFaultParams {
	nfp := NetworkFaultParams{}

	if v, ok := params["device"].(string); ok {
		nfp.Device = v
	}
	if v, ok := params["latency"].(int); ok {
		nfp.Latency = v
	}
	if v, ok := params["packet_loss"].(float64); ok {
		nfp.PacketLoss = v
	} else if v, ok := params["packet_loss"].(int); ok {
		nfp.PacketLoss = float64(v)
	} else if v, ok := params["packet_loss"].(string); ok {
		// Handle "50%" format
		var pct float64
		if _, err := fmt.Sscanf(v, "%f%%", &pct); err == nil {
			nfp.PacketLoss = pct
		}
	}
	if v, ok := params["bandwidth"].(int); ok {
		nfp.Bandwidth = v
	}
	if v, ok := params["target_ports"].(string); ok {
		nfp.TargetPorts = v
	}
	if v, ok := params["target_proto"].(string); ok {
		nfp.TargetProto = v
	}
	return nfp
}

