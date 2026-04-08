// Package corruption implements a JSON-aware HTTP reverse proxy for semantic
// fault injection. It parses and mutates JSON responses using the full
// standard library, with support for stateful corruption rules and a
// dynamic control API.
package corruption

import (
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// CorruptionRule describes a single mutation rule applied to matching responses.
// It is a plain value type (no sync primitives) so it can be freely copied.
type CorruptionRule struct {
	// Name is a human-readable identifier used in logs and stats.
	Name string `yaml:"name"`

	// PathPattern is a regular expression matched against the HTTP request path.
	// For JSON-RPC endpoints (Bor port 8545) the path is always "/", so use
	// MethodMatch to discriminate instead.
	PathPattern string `yaml:"path_pattern"`

	// MethodMatch is matched against the "method" field in a JSON-RPC request
	// body. Ignored when empty. When set, the proxy reads the request body and
	// tests this regex before applying the rule.
	MethodMatch string `yaml:"method_match"`

	// Probability is the fraction of matching requests that are corrupted.
	// 0.0 means never corrupt; 1.0 (default, used when omitted) means always
	// corrupt. A pointer is used so a missing YAML key (nil) can be
	// distinguished from an explicit 0.0 ("never").
	Probability *float64 `yaml:"probability,omitempty"`

	// Operations is the ordered list of mutations to apply.
	Operations []CorruptionOp `yaml:"operations"`

	// Stateful controls counter-based corruption patterns.
	Stateful *StatefulConfig `yaml:"stateful,omitempty"`
}

// CorruptionOp is a single atomic mutation on a parsed JSON object.
type CorruptionOp struct {
	// Type selects the mutation function:
	//   "set_field"      – set a nested field to Value
	//   "delete_field"   – remove a nested field
	//   "corrupt_base64" – flip bits in a base64-encoded string field
	//   "truncate_array" – truncate an array field to Value (int) elements
	//   "inject_error"   – replace entire response with a JSON-RPC error object
	Type string `yaml:"type"`

	// Field is a dot-notation JSON path, e.g. "result.hash" or
	// "result.selectedProducers". Ignored for "inject_error".
	Field string `yaml:"field"`

	// Value is the replacement value used by "set_field" and "truncate_array".
	// For "truncate_array" it must be an integer (or be omitted to mean 0).
	// For "set_field" it may be any YAML-representable value.
	Value interface{} `yaml:"value"`
}

// StatefulConfig controls counter-based corruption behaviour. All counters are
// per-rule and reset only on an explicit POST /reset to the control API.
type StatefulConfig struct {
	// CorruptEveryN corrupts exactly every Nth matching request (1-indexed).
	// 0 or 1 means corrupt all.
	CorruptEveryN int `yaml:"corrupt_every_n"`

	// SkipFirst skips the first N matching requests before any corruption.
	SkipFirst int `yaml:"skip_first"`

	// MaxCorrupted stops corrupting after this many corruptions. 0 = unlimited.
	MaxCorrupted int `yaml:"max_corrupted"`
}

// compiledRule holds pre-compiled regex values for fast matching.
type compiledRule struct {
	pathRe   *regexp.Regexp // nil if PathPattern is ""
	methodRe *regexp.Regexp // nil if MethodMatch is ""
}

// compileRule compiles the regular expressions in r, returning an error if
// any regex is invalid. Called eagerly in Load so bad configs are rejected
// before going live.
func compileRule(r *CorruptionRule) (*compiledRule, error) {
	c := &compiledRule{}
	if r.PathPattern != "" {
		re, err := regexp.Compile(r.PathPattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: invalid path_pattern %q: %w", r.Name, r.PathPattern, err)
		}
		c.pathRe = re
	}
	if r.MethodMatch != "" {
		re, err := regexp.Compile(r.MethodMatch)
		if err != nil {
			return nil, fmt.Errorf("rule %q: invalid method_match %q: %w", r.Name, r.MethodMatch, err)
		}
		c.methodRe = re
	}
	return c, nil
}

// effectiveProbability returns the configured probability, or 1.0 when the
// field was omitted from the YAML (nil = "always corrupt" default).
func (r *CorruptionRule) effectiveProbability() float64 {
	if r.Probability == nil {
		return 1.0
	}
	return *r.Probability
}

// RuleSet is a thread-safe, hot-swappable collection of CorruptionRules with
// per-rule statistics. The entire set is replaced atomically on updates so
// in-flight requests always see a consistent snapshot.
type RuleSet struct {
	mu    sync.RWMutex
	rules []*ruleWithState
}

// ruleWithState pairs an immutable rule (and its pre-compiled regexes) with
// live atomic counters. It is heap-allocated so the rule slice can be swapped
// atomically without copying sync primitives.
type ruleWithState struct {
	rule     CorruptionRule
	compiled *compiledRule

	// seen counts all requests that matched this rule (before probability /
	// stateful filtering).
	seen atomic.Int64

	// corrupted counts requests that were actually mutated.
	corrupted atomic.Int64
}

// matchesPath returns true when the rule's PathPattern matches path.
// A rule with an empty PathPattern matches every path.
func (rs *ruleWithState) matchesPath(path string) bool {
	if rs.compiled.pathRe == nil {
		return true
	}
	return rs.compiled.pathRe.MatchString(path)
}

// matchesMethod returns true when the rule's MethodMatch matches method.
// When MethodMatch is empty the rule matches regardless of the method field.
func (rs *ruleWithState) matchesMethod(method string) bool {
	if rs.compiled.methodRe == nil {
		return true
	}
	return rs.compiled.methodRe.MatchString(method)
}

// Load replaces the current rule set by parsing rulesYAML. The replacement is
// atomic — concurrent readers always see either the old or the new set in full.
func (rs *RuleSet) Load(rulesYAML []byte) error {
	var rules []CorruptionRule
	if err := yaml.Unmarshal(rulesYAML, &rules); err != nil {
		return fmt.Errorf("failed to parse rules YAML: %w", err)
	}

	states := make([]*ruleWithState, len(rules))
	for i := range rules {
		r := &rules[i]
		// Validate regexes eagerly so Load returns an error on bad config
		// rather than silently skipping rules at runtime.
		c, err := compileRule(r)
		if err != nil {
			return err
		}
		if r.Probability != nil && (*r.Probability < 0 || *r.Probability > 1) {
			return fmt.Errorf("rule %q: probability must be between 0.0 and 1.0, got %f", r.Name, *r.Probability)
		}
		states[i] = &ruleWithState{rule: *r, compiled: c}
	}

	rs.mu.Lock()
	rs.rules = states
	rs.mu.Unlock()
	return nil
}

// Snapshot returns a copy of the current rule states for stats reporting or
// YAML serialisation. The returned slice is safe to read without holding any lock.
func (rs *RuleSet) Snapshot() []RuleSnapshot {
	rs.mu.RLock()
	states := rs.rules
	rs.mu.RUnlock()

	out := make([]RuleSnapshot, len(states))
	for i, s := range states {
		out[i] = RuleSnapshot{
			Name:      s.rule.Name,
			Seen:      s.seen.Load(),
			Corrupted: s.corrupted.Load(),
		}
	}
	return out
}

// Reset resets all per-rule counters without changing the rules themselves.
func (rs *RuleSet) Reset() {
	rs.mu.RLock()
	states := rs.rules
	rs.mu.RUnlock()

	for _, s := range states {
		s.seen.Store(0)
		s.corrupted.Store(0)
	}
}

// MarshalYAML serialises the current rules back to YAML for GET /rules.
func (rs *RuleSet) MarshalYAML() ([]byte, error) {
	rs.mu.RLock()
	states := rs.rules
	rs.mu.RUnlock()

	rules := make([]CorruptionRule, len(states))
	for i, s := range states {
		rules[i] = s.rule
	}
	return yaml.Marshal(rules)
}

// RuleSnapshot is a point-in-time view of a rule's counters, safe to read
// without holding any locks.
type RuleSnapshot struct {
	Name      string `json:"name"`
	Seen      int64  `json:"seen"`
	Corrupted int64  `json:"corrupted"`
}

// ValidateRules parses the YAML and checks all regexes without mutating the
// RuleSet. Useful for dry-run validation before committing a new config.
func ValidateRules(rulesYAML []byte) error {
	var tmp RuleSet
	return tmp.Load(rulesYAML)
}
