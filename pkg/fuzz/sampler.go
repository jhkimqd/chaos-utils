// Package fuzz implements randomized chaos scenario generation and execution
// with near-threshold parameter sampling and compound fault support.
package fuzz

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/jihwankim/chaos-utils/pkg/fuzz/precompile"
	"github.com/jihwankim/chaos-utils/pkg/scenario"
)

// FaultSpec is a fully-resolved fault: parameters and a human-readable slug.
type FaultSpec struct {
	FaultType  string                 `json:"fault_type"`
	TargetTier string                 `json:"target_tier"`
	Params     map[string]interface{} `json:"params"`
	Slug       string                 `json:"slug"` // e.g. "44pct-loss", "328ms-latency"
}

// TierInfo describes a target tier and its service selector patterns.
type TierInfo struct {
	Label     string
	Selectors []SelectorInfo
}

// SelectorInfo maps a Kurtosis service name pattern to a target alias.
type SelectorInfo struct {
	Pattern string
	Alias   string
}

// tierNamespaces maps each tier to the container namespaces it touches.
// Two tiers that share a namespace cannot be safely combined in one scenario:
// concurrent tc/iptables injection on the same container causes "File exists" errors.
var tierNamespaces = map[string][]string{
	"validator1_heimdall": {"heimdall"},
	"validator1_bor":      {"bor"},
	"validator1_both":     {"heimdall", "bor"},
	"all_heimdall":        {"heimdall"},
	"all_bor":             {"bor"},
	"all_both":            {"heimdall", "bor"},
	"rabbitmq":            {"rabbitmq"},
	"rpc_nodes":           {"rpc"},
}

// TargetTiers defines all available target tiers for fault injection.
var TargetTiers = map[string]TierInfo{
	"validator1_heimdall": {
		Label:     "validator 1 — Heimdall consensus",
		Selectors: []SelectorInfo{{"l2-cl-1-heimdall-v2-bor-validator", "target_heimdall"}},
	},
	"validator1_bor": {
		Label:     "validator 1 — Bor execution",
		Selectors: []SelectorInfo{{"l2-el-1-bor-heimdall-v2-validator", "target_bor"}},
	},
	"validator1_both": {
		Label: "validator 1 — Heimdall + Bor",
		Selectors: []SelectorInfo{
			{"l2-cl-1-heimdall-v2-bor-validator", "target_heimdall"},
			{"l2-el-1-bor-heimdall-v2-validator", "target_bor"},
		},
	},
	"all_heimdall": {
		Label:     "all validators — Heimdall consensus",
		Selectors: []SelectorInfo{{"l2-cl-.*-heimdall-v2-bor-validator", "target_heimdall"}},
	},
	"all_bor": {
		Label:     "all validators — Bor execution",
		Selectors: []SelectorInfo{{"l2-el-.*-bor-heimdall-v2-validator", "target_bor"}},
	},
	"all_both": {
		Label: "all validators — Heimdall + Bor",
		Selectors: []SelectorInfo{
			{"l2-cl-.*-heimdall-v2-bor-validator", "target_heimdall"},
			{"l2-el-.*-bor-heimdall-v2-validator", "target_bor"},
		},
	},
	"rabbitmq": {
		Label:     "RabbitMQ — Heimdall event bus",
		Selectors: []SelectorInfo{{"l2-cl-.*-rabbitmq", "target_rabbitmq"}},
	},
	"rpc_nodes": {
		Label:     "Bor RPC-only nodes",
		Selectors: []SelectorInfo{{"l2-el-.*-bor-heimdall-v2-rpc", "target_rpc"}},
	},
}

// faultTypes is the ordered list of supported fault kinds.
var faultTypes = []string{
	"packet_loss",
	"latency",
	"bandwidth_throttle",
	"packet_reorder",
	"connection_drop",
	"dns_latency",
	"container_restart",
	"container_pause",
	"cpu_stress",
	"memory_pressure",
	"disk_io",
}

// tierNames is a sorted, stable slice of tier map keys for deterministic sampling.
var tierNames []string

func init() {
	for k := range TargetTiers {
		tierNames = append(tierNames, k)
	}
	sort.Strings(tierNames)
}

// Sampler holds a seeded RNG and produces FaultSpecs with near-threshold parameters.
type Sampler struct {
	rng *rand.Rand
}

// NewSampler creates a Sampler seeded with the given value.
func NewSampler(seed int64) *Sampler {
	return &Sampler{rng: rand.New(rand.NewSource(seed))} //nolint:gosec
}

// triangular samples from a triangular distribution on [lo, hi] with the given mode.
func (s *Sampler) triangular(lo, hi, mode float64) float64 {
	u := s.rng.Float64()
	fc := (mode - lo) / (hi - lo)
	if u < fc {
		return lo + math.Sqrt(u*(hi-lo)*(mode-lo))
	}
	return hi - math.Sqrt((1-u)*(hi-lo)*(hi-mode))
}

// logUniform samples uniformly in log-space on [lo, hi], returning the nearest int.
func (s *Sampler) logUniform(lo, hi float64) int {
	return int(math.Exp(s.rng.Float64()*(math.Log(hi)-math.Log(lo)) + math.Log(lo)))
}

// weightedChoice picks one element from choices according to integer weights.
func (s *Sampler) weightedChoice(choices []int, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	r := s.rng.Intn(total)
	for i, w := range weights {
		r -= w
		if r < 0 {
			return choices[i]
		}
	}
	return choices[len(choices)-1]
}

// SampleFault returns parameters and a slug for the given fault type.
// Ranges are biased toward the near-threshold zone where protocol bugs are most likely.
func (s *Sampler) SampleFault(faultType string) (map[string]interface{}, string) {
	switch faultType {
	case "packet_loss":
		// Triangular biased toward 55% — stresses consensus timeout re-election.
		v := int(s.triangular(25, 75, 55))
		return map[string]interface{}{
			"packet_loss":  v,
			"target_proto": "tcp,udp",
			"device":       "eth0",
		}, fmt.Sprintf("%dpct-loss", v)

	case "latency":
		// Log-uniform 300ms–5s — crosses Tendermint round-trip boundaries.
		v := s.logUniform(300, 5000)
		return map[string]interface{}{
			"latency":      v,
			"target_proto": "tcp,udp",
			"device":       "eth0",
		}, fmt.Sprintf("%dms-latency", v)

	case "bandwidth_throttle":
		// Log-uniform 200kbps–5Mbps — between gossip-breaking and barely-noticeable.
		v := s.logUniform(200, 5000)
		return map[string]interface{}{
			"bandwidth":    v,
			"target_proto": "tcp,udp",
			"device":       "eth0",
		}, fmt.Sprintf("%dkbps-bw", v)

	case "packet_reorder":
		// Uniform 30–70% reorder — disrupts TCP ordering without full loss.
		v := int(s.rng.Float64()*40 + 30)
		return map[string]interface{}{
			"reorder":      v,
			"latency":      10,
			"target_proto": "tcp,udp",
			"device":       "eth0",
		}, fmt.Sprintf("%dpct-reorder", v)

	case "connection_drop":
		// Triangular biased toward 45% — partial connectivity stress.
		v := int(s.triangular(20, 70, 45))
		return map[string]interface{}{
			"probability":  float64(v) / 100.0,
			"target_proto": "tcp",
			"rule_type":    "drop",
		}, fmt.Sprintf("%dpct-drop", v)

	case "dns_latency":
		// Log-uniform 500ms–6s with optional failure rate.
		v := s.logUniform(500, 6000)
		f := math.Round(s.rng.Float64()*0.3*100) / 100 // 0.00–0.30
		return map[string]interface{}{
			"delay_ms":     v,
			"failure_rate": f,
		}, fmt.Sprintf("%dms-dns", v)

	case "container_restart":
		// Weighted toward 0s/1s grace — tests abrupt process recovery.
		v := s.weightedChoice([]int{0, 1, 5, 10}, []int{4, 3, 2, 1})
		return map[string]interface{}{
			"grace_period": v,
		}, fmt.Sprintf("%ds-grace-restart", v)

	case "container_pause":
		// Triangular biased toward 45s — crosses consensus round duration.
		v := int(s.triangular(15, 120, 45))
		return map[string]interface{}{
			"duration": fmt.Sprintf("%ds", v),
			"unpause":  true,
		}, fmt.Sprintf("%ds-pause", v)

	case "cpu_stress":
		// Triangular biased toward 75% — leaves some headroom for OS scheduler.
		v := int(s.triangular(55, 92, 75))
		return map[string]interface{}{
			"cpu_percent": v,
		}, fmt.Sprintf("%dpct-cpu", v)

	case "memory_pressure":
		// Uniform 300–900 MB — between trivial and OOM-killer territory.
		v := int(s.rng.Float64()*600 + 300)
		return map[string]interface{}{
			"memory_mb": v,
		}, fmt.Sprintf("%dmb-mem", v)

	case "disk_io":
		// Log-uniform 200ms–3s — stresses WAL and state-db write paths.
		v := s.logUniform(200, 3000)
		return map[string]interface{}{
			"io_latency_ms": v,
			"operation":     "all",
		}, fmt.Sprintf("%dms-disk", v)

	default:
		return map[string]interface{}{}, "unknown"
	}
}

// SampleSingle returns one fault on a randomly chosen target tier.
func (s *Sampler) SampleSingle() []FaultSpec {
	ft := faultTypes[s.rng.Intn(len(faultTypes))]
	tt := tierNames[s.rng.Intn(len(tierNames))]
	params, slug := s.SampleFault(ft)
	return []FaultSpec{{FaultType: ft, TargetTier: tt, Params: params, Slug: slug}}
}

// SampleMulti returns up to n faults, each targeting a namespace-disjoint tier.
// Tiers that share container namespaces (e.g. all_both and all_heimdall both touch
// heimdall containers) are never combined — doing so would cause concurrent tc/iptables
// commands to collide on the same network interface.
// The actual count may be less than n if not enough disjoint tiers are available.
func (s *Sampler) SampleMulti(n int) []FaultSpec {
	if n < 1 {
		n = 1
	}

	// Shuffle tier names for a random starting order.
	shuffled := make([]string, len(tierNames))
	copy(shuffled, tierNames)
	s.rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	// Greedily pick tiers whose namespaces don't overlap with already-selected ones.
	selected := make([]string, 0, n)
	usedNamespaces := map[string]bool{}

	for _, tt := range shuffled {
		if len(selected) >= n {
			break
		}
		conflict := false
		for _, ns := range tierNamespaces[tt] {
			if usedNamespaces[ns] {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		selected = append(selected, tt)
		for _, ns := range tierNamespaces[tt] {
			usedNamespaces[ns] = true
		}
	}

	// Build fault specs from the selected non-overlapping tiers.
	specs := make([]FaultSpec, len(selected))
	usedTypes := map[string]bool{}

	for i, tt := range selected {
		// Prefer a fault type not already used in this scenario.
		available := make([]string, 0, len(faultTypes))
		for _, ft := range faultTypes {
			if !usedTypes[ft] {
				available = append(available, ft)
			}
		}
		if len(available) == 0 {
			available = faultTypes
		}
		ft := available[s.rng.Intn(len(available))]
		usedTypes[ft] = true

		params, slug := s.SampleFault(ft)
		specs[i] = FaultSpec{FaultType: ft, TargetTier: tt, Params: params, Slug: slug}
	}
	return specs
}

// Sample returns specs based on compoundBias and maxFaults.
// When compound is chosen, the number of simultaneous faults is drawn uniformly
// from [2, maxFaults] so pressure varies across rounds.
// maxFaults < 2 forces single-fault mode regardless of bias.
func (s *Sampler) Sample(compoundBias float64, maxFaults int) []FaultSpec {
	if maxFaults < 2 || s.rng.Float64() >= compoundBias {
		return s.SampleSingle()
	}
	// Pick n ∈ [2, maxFaults] — varies pressure round to round.
	n := 2 + s.rng.Intn(maxFaults-1)
	return s.SampleMulti(n)
}

// SamplePrecompileCriteria returns two rpc-type success criteria per round:
//  1. A randomly sampled known precompile from the registry (exact/non_empty check)
//  2. A randomly generated address in [0x0a, 0xffff] that must return empty
//
// Both criteria are non-critical so they never abort an experiment — the RPC node
// may itself be under fault injection. The caller provides rpcURL which is embedded
// as the evaluator endpoint in each criterion's URL field for the address, but the
// endpoint itself is stored on the detector's rpcClient.
//
// The criterion URL field holds the *target contract address* (the "to" in eth_call).
func (s *Sampler) SamplePrecompileCriteria(rpcURL string) []scenario.SuccessCriterion {
	all := precompile.All()
	if len(all) == 0 || rpcURL == "" {
		return nil
	}

	entry := all[s.rng.Intn(len(all))]
	randomAddr := precompile.SampleRandomInvalidAddress(s.rng)

	// Shorten the random address for the criterion name (last 4 hex chars).
	shortAddr := randomAddr[len(randomAddr)-4:]

	return []scenario.SuccessCriterion{
		{
			Name:        fmt.Sprintf("precompile-%s-invariant", entry.Name),
			Description: fmt.Sprintf("EVM precompile %s (%s) returns correct output after fault removal", entry.Name, entry.Address),
			Type:        "rpc",
			URL:         entry.Address,
			RPCMethod:   "eth_call",
			RPCCallData: entry.Input,
			RPCExpected: entry.Expected,
			RPCCheck:    entry.Check,
			Critical:    false, // non-critical: RPC node may be under fault
		},
		{
			Name:        fmt.Sprintf("random-addr-0x%s-empty", shortAddr),
			Description: fmt.Sprintf("Address %s has no deployed code (not a precompile or system contract)", randomAddr),
			Type:        "rpc",
			URL:         randomAddr,
			RPCMethod:   "eth_call",
			RPCCallData: "0x",
			RPCCheck:    "empty",
			Critical:    false,
		},
	}
}
