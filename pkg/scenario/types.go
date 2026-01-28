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
	// Type of selector: kurtosis_service, docker_container, manual
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
}

// SuccessCriterion defines a success criterion for the test
type SuccessCriterion struct {
	// Name of the criterion
	Name string `yaml:"name"`

	// Description of what this checks
	Description string `yaml:"description,omitempty"`

	// Type: prometheus, health_check, custom
	Type string `yaml:"type"`

	// Query for Prometheus-based criteria
	Query string `yaml:"query,omitempty"`

	// Threshold to compare against (e.g., "> 0", "< 100", "== 0")
	Threshold string `yaml:"threshold,omitempty"`

	// Window is the time window for evaluation
	Window time.Duration `yaml:"window,omitempty"`

	// URL for health check criteria
	URL string `yaml:"url,omitempty"`

	// ExpectedStatus for HTTP health checks
	ExpectedStatus int `yaml:"expected_status,omitempty"`

	// Critical marks this as a critical criterion (test fails if this fails)
	Critical bool `yaml:"critical,omitempty"`
}

// NetworkFaultParams defines parameters for network faults
type NetworkFaultParams struct {
	Device      string  `yaml:"device,omitempty"`
	Latency     int     `yaml:"latency,omitempty"`
	Jitter      int     `yaml:"jitter,omitempty"`
	PacketLoss  float64 `yaml:"packet_loss,omitempty"`
	Bandwidth   int     `yaml:"bandwidth,omitempty"`
	TargetPorts string  `yaml:"target_ports,omitempty"`
	TargetProto string  `yaml:"target_proto,omitempty"`
	TargetIPs   string  `yaml:"target_ips,omitempty"`
	TargetCIDR  string  `yaml:"target_cidr,omitempty"`
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
	if v, ok := params["jitter"].(int); ok {
		nfp.Jitter = v
	}
	if v, ok := params["packet_loss"].(float64); ok {
		nfp.PacketLoss = v
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
	if v, ok := params["target_ips"].(string); ok {
		nfp.TargetIPs = v
	}
	if v, ok := params["target_cidr"].(string); ok {
		nfp.TargetCIDR = v
	}

	return nfp
}
