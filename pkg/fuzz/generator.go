package fuzz

import (
	"fmt"
	"strings"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// invariantCriteria are topology-safe success criteria shared by all fuzz scenarios.
// Ratio-based PromQL expressions are N-validator agnostic.
var invariantCriteria = []scenario.SuccessCriterion{
	{
		Name:        "block_production_continues",
		Description: "Network continues producing Bor blocks after the fault is removed",
		Type:        "prometheus",
		// sum() aggregates across all validators so a single missed scrape doesn't
		// empty the vector. or vector(0) converts a fully-empty result (all scrapes
		// missing) to 0 so the threshold comparison fires instead of "no results".
		// 5m window captures post-teardown recovery; evaluated after TEARDOWN so
		// network faults no longer block Prometheus scraping.
		Query:     `sum(increase(chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}[5m])) or vector(0)`,
		Threshold: "> 0",
		Critical:  true,
	},
	{
		Name:        "consensus_height_advances",
		Description: "Heimdall consensus height continues to increase after the fault is removed",
		Type:        "prometheus",
		Query:       `sum(increase(cometbft_consensus_height{job=~"l2-cl-.*-heimdall-v2-bor-validator"}[5m])) or vector(0)`,
		Threshold:   "> 0",
		Critical:    true,
	},
	{
		Name:        "bft_quorum_maintained",
		Description: "At least 2/3 of validators remain online (topology-safe BFT quorum ratio)",
		Type:        "prometheus",
		Query:       `count(up{job=~"l2-cl-.*-heimdall-v2-bor-validator"} == 1) / scalar(count(up{job=~"l2-cl-.*-heimdall-v2-bor-validator"}))`,
		Threshold:   ">= 0.67",
		Critical:    true,
	},
}

// faultRunnerType maps logical fault names to the type strings the injector understands.
func faultRunnerType(faultType string) string {
	switch faultType {
	case "packet_loss", "latency", "bandwidth_throttle", "packet_reorder":
		return "network"
	case "connection_drop":
		return "connection_drop"
	case "dns_latency":
		return "dns"
	case "memory_pressure":
		return "memory_stress"
	default:
		// container_restart, container_pause, cpu_stress, disk_io map 1:1
		return faultType
	}
}

// timingForFaults returns duration/warmup/cooldown suited to the fault mix.
// Compound faults get the longest window; resource faults need recovery time.
func timingForFaults(specs []FaultSpec) (duration, warmup, cooldown time.Duration) {
	if len(specs) > 1 {
		return 10 * time.Minute, 2 * time.Minute, 2 * time.Minute
	}
	switch specs[0].FaultType {
	case "latency", "packet_loss", "packet_reorder", "connection_drop":
		return 7 * time.Minute, 90 * time.Second, 60 * time.Second
	case "cpu_stress", "memory_pressure", "disk_io":
		return 8 * time.Minute, 2 * time.Minute, 2 * time.Minute
	default:
		return 5 * time.Minute, 60 * time.Second, 60 * time.Second
	}
}

// BuildScenario constructs a *scenario.Scenario from sampled fault specs.
// The enclave name is embedded directly — no ${ENCLAVE_NAME} placeholder needed.
// Returns the scenario and its generated name.
func BuildScenario(specs []FaultSpec, enclave string) (*scenario.Scenario, string) {
	duration, warmup, cooldown := timingForFaults(specs)

	// Collect unique target selectors, preserving first-seen order.
	seen := map[string]bool{}
	var targets []scenario.Target
	for _, spec := range specs {
		for _, sel := range TargetTiers[spec.TargetTier].Selectors {
			if !seen[sel.Alias] {
				seen[sel.Alias] = true
				targets = append(targets, scenario.Target{
					Selector: scenario.TargetSelector{
						Type:    "kurtosis_service",
						Enclave: enclave,
						Pattern: sel.Pattern,
					},
					Alias: sel.Alias,
				})
			}
		}
	}

	// Build fault entries. Multi-selector tiers (e.g. all_both) produce one
	// entry per selector so each container receives an independent injection.
	var faults []scenario.Fault
	for i, spec := range specs {
		for _, sel := range TargetTiers[spec.TargetTier].Selectors {
			// Defensive copy of params so each fault entry owns its own map.
			params := make(map[string]interface{}, len(spec.Params))
			for k, v := range spec.Params {
				params[k] = v
			}
			faults = append(faults, scenario.Fault{
				Phase: fmt.Sprintf(
					"fault%d-%s-%s",
					i+1,
					strings.ReplaceAll(spec.FaultType, "_", "-"),
					sel.Alias,
				),
				Description: fmt.Sprintf(
					"%s applied to %s",
					spec.Slug,
					TargetTiers[spec.TargetTier].Label,
				),
				Target: sel.Alias,
				Type:   faultRunnerType(spec.FaultType),
				Params: params,
			})
		}
	}

	// Attach window = duration to each invariant criterion.
	criteria := make([]scenario.SuccessCriterion, len(invariantCriteria))
	for i, c := range invariantCriteria {
		c.Window = duration
		criteria[i] = c
	}

	// Compose scenario name.
	parts := []string{"fuzz"}
	for _, spec := range specs {
		parts = append(parts, spec.Slug, strings.ReplaceAll(spec.TargetTier, "_", "-"))
	}
	name := strings.Join(parts, "-")
	if len(name) > 80 {
		name = name[:77] + "..."
	}

	// Compose description.
	descParts := make([]string, len(specs))
	for i, spec := range specs {
		descParts[i] = fmt.Sprintf("%s on %s", spec.Slug, TargetTiers[spec.TargetTier].Label)
	}
	description := strings.Join(descParts, " + ") + ". Randomized fuzz scenario."

	tags := []string{"fuzz", "generated"}
	for _, spec := range specs {
		tags = append(tags, strings.ReplaceAll(spec.FaultType, "_", "-"))
	}

	sc := &scenario.Scenario{
		APIVersion: "chaos.polygon.io/v1",
		Kind:       "ChaosScenario",
		Metadata: scenario.Metadata{
			Name:        name,
			Description: description,
			Tags:        tags,
			Author:      "chaos-runner fuzz",
			Version:     "0.0.1",
		},
		Spec: scenario.ScenarioSpec{
			Targets:         targets,
			Duration:        duration,
			Warmup:          warmup,
			Cooldown:        cooldown,
			Faults:          faults,
			SuccessCriteria: criteria,
			Metrics: []string{
				"chain_head_block",
				"cometbft_consensus_height",
				"cometbft_consensus_validators",
				"up",
			},
		},
	}
	return sc, name
}
