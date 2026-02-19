package precompile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Result holds the outcome of a single precompile test call.
type Result struct {
	// Address is the contract/precompile address that was called.
	Address string `json:"address"`
	// Name is the human-readable label from the registry.
	Name string `json:"name"`
	// Passed is true when the actual return value matched the expected check.
	Passed bool `json:"passed"`
	// Message describes the outcome.
	Message string `json:"message"`
	// Category is "known_precompile" or "invalid_address".
	Category string `json:"category"`
}

// Fuzzer runs randomized EVM precompile invariant checks.
type Fuzzer struct {
	rpcURL string
	rng    *rand.Rand
	client *http.Client
}

// New creates a Fuzzer that connects to rpcURL and uses the given seed for reproducibility.
func New(rpcURL string, seed int64) *Fuzzer {
	return &Fuzzer{
		rpcURL: rpcURL,
		rng:    rand.New(rand.NewSource(seed)), //nolint:gosec
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// RunRound picks one random entry from KnownPrecompiles (or PolygonPrecompiles) and one
// random entry from InvalidAddresses, calls both via eth_call, and returns the two results.
// It never returns an error — failures are captured in Result.Passed and Result.Message.
func (f *Fuzzer) RunRound(ctx context.Context) []Result {
	results := make([]Result, 0, 2)

	// Pick a random "must work" precompile.
	known := All()
	if len(known) > 0 {
		entry := known[f.rng.Intn(len(known))]
		results = append(results, f.check(ctx, entry, "known_precompile"))
	}

	// Pick a random "must not work" address.
	if len(InvalidAddresses) > 0 {
		entry := InvalidAddresses[f.rng.Intn(len(InvalidAddresses))]
		results = append(results, f.check(ctx, entry, "invalid_address"))
	}

	return results
}

// RunAll calls every entry in the full registry (known + invalid) and returns all results.
// Useful for a comprehensive one-shot audit rather than sampling.
func (f *Fuzzer) RunAll(ctx context.Context) []Result {
	var results []Result
	for _, e := range All() {
		results = append(results, f.check(ctx, e, "known_precompile"))
	}
	for _, e := range InvalidAddresses {
		results = append(results, f.check(ctx, e, "invalid_address"))
	}
	return results
}

// check executes one eth_call and validates the result against the entry's Check rule.
func (f *Fuzzer) check(ctx context.Context, entry Entry, category string) Result {
	got, err := f.ethCall(ctx, entry.Address, entry.Input)
	if err != nil {
		return Result{
			Address:  entry.Address,
			Name:     entry.Name,
			Passed:   false,
			Message:  fmt.Sprintf("eth_call error: %v", err),
			Category: category,
		}
	}

	var passed bool
	var msg string

	switch entry.Check {
	case "exact":
		passed = (got == entry.Expected)
		if passed {
			msg = fmt.Sprintf("returned expected value %s", got)
		} else {
			msg = fmt.Sprintf("got %s; want %s", got, entry.Expected)
		}

	case "non_empty":
		passed = (got != "" && got != "0x")
		if passed {
			msg = fmt.Sprintf("returned non-empty value (len=%d)", len(got))
		} else {
			msg = "returned empty — precompile not active or address has no code"
		}

	case "empty":
		passed = (got == "" || got == "0x")
		if passed {
			msg = "correctly returned empty (no code at address)"
		} else {
			msg = fmt.Sprintf("unexpectedly returned non-empty value %s — unknown code deployed at this address", got)
		}

	default:
		msg = fmt.Sprintf("unknown check type %q in registry entry for %s", entry.Check, entry.Name)
	}

	return Result{
		Address:  entry.Address,
		Name:     entry.Name,
		Passed:   passed,
		Message:  msg,
		Category: category,
	}
}

// ── minimal JSON-RPC client ──────────────────────────────────────────────────

type ethCallRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type ethCallResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ethCall calls eth_call({to, data}, "latest") and returns the hex result string.
func (f *Fuzzer) ethCall(ctx context.Context, to, data string) (string, error) {
	req := ethCallRequest{
		JSONRPC: "2.0",
		Method:  "eth_call",
		Params: []interface{}{
			map[string]string{"to": to, "data": data},
			"latest",
		},
		ID: 1,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.rpcURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	var rpcResp ethCallResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if rpcResp.Error != nil {
		return "", fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Unwrap JSON string `"0xdeadbeef"` → `0xdeadbeef`.
	if len(rpcResp.Result) >= 2 && rpcResp.Result[0] == '"' {
		var s string
		if err := json.Unmarshal(rpcResp.Result, &s); err != nil {
			return "", fmt.Errorf("unmarshal result string: %w", err)
		}
		return s, nil
	}
	return string(rpcResp.Result), nil
}
