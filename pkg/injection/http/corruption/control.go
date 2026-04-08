package corruption

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ControlServer exposes a small HTTP API for dynamic rule management.
// It is intentionally minimal — a single mux with four routes.
//
// POST /rules  — replace all rules (accepts YAML body)
// GET  /rules  — return current rules as YAML
// GET  /stats  — return corruption statistics as JSON
// POST /reset  — reset statistics and stateful counters
type ControlServer struct {
	rules *RuleSet
	proxy *CorruptionProxy
}

// NewControlServer wires the rule set and proxy stats to the control handler.
func NewControlServer(rules *RuleSet, proxy *CorruptionProxy) *ControlServer {
	return &ControlServer{rules: rules, proxy: proxy}
}

// Handler returns an http.Handler suitable for http.ListenAndServe.
func (cs *ControlServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rules", cs.handleRules)
	mux.HandleFunc("/stats", cs.handleStats)
	mux.HandleFunc("/reset", cs.handleReset)
	return mux
}

// handleRules dispatches GET and POST on /rules.
func (cs *ControlServer) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cs.getRules(w, r)
	case http.MethodPost:
		cs.postRules(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// getRules returns the current rule set serialised as YAML.
func (cs *ControlServer) getRules(w http.ResponseWriter, _ *http.Request) {
	data, err := cs.rules.MarshalYAML()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to serialise rules: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(data)
}

// postRules replaces all rules from a YAML body.
// The request body is limited to 1 MiB to protect against runaway uploads.
func (cs *ControlServer) postRules(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := cs.rules.Load(body); err != nil {
		http.Error(w, fmt.Sprintf("invalid rules: %v", err), http.StatusBadRequest)
		return
	}

	fmt.Printf("[CORRUPT] control: rules updated\n")
	w.WriteHeader(http.StatusNoContent)
}

// handleStats returns aggregate proxy counters as JSON.
func (cs *ControlServer) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats := cs.proxy.Stats()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(stats); err != nil {
		// Response already partially written; best effort.
		fmt.Printf("[CORRUPT] control: failed to encode stats: %v\n", err)
	}
}

// handleReset resets all per-rule counters and proxy aggregate totals.
func (cs *ControlServer) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cs.rules.Reset()
	cs.proxy.stats.requestsTotal.Store(0)
	cs.proxy.stats.requestsCorrupted.Store(0)
	cs.proxy.stats.requestsPassThru.Store(0)

	fmt.Printf("[CORRUPT] control: stats and counters reset\n")
	w.WriteHeader(http.StatusNoContent)
}
