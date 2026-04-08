package corruption

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// startFakeBackend starts a test HTTP server that returns a fixed JSON body.
// The caller is responsible for calling ts.Close().
func startFakeBackend(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// startProxy builds a CorruptionProxy targeting backendURL with the given YAML
// rules and wraps it in an httptest.Server.
func startProxy(t *testing.T, backendURL string, rulesYAML string) (*httptest.Server, *CorruptionProxy) {
	t.Helper()
	parsed, err := url.Parse(backendURL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}

	rules := &RuleSet{}
	if rulesYAML != "" {
		if err := rules.Load([]byte(rulesYAML)); err != nil {
			t.Fatalf("load rules: %v", err)
		}
	}

	proxy := NewProxy(parsed, rules)
	ts := httptest.NewServer(proxy)
	t.Cleanup(ts.Close)
	return ts, proxy
}

// doGet sends a GET request to url and returns the parsed JSON body.
func doGet(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal response body %q: %v", body, err)
	}
	return m
}

// doPost sends a POST with a JSON body and returns the parsed JSON response.
func doPost(t *testing.T, proxyURL string, payload string) map[string]interface{} {
	t.Helper()
	resp, err := http.Post(proxyURL, "application/json", strings.NewReader(payload)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST %s: %v", proxyURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal response body %q: %v", body, err)
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Integration tests
// ─────────────────────────────────────────────────────────────────────────────

// TestProxy_PassThrough verifies that requests with no matching rule are
// forwarded unmodified.
func TestProxy_PassThrough(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"root_hash":"AAAA=="}}`))
	}))

	// Empty rule set → no corruption.
	proxyTS, _ := startProxy(t, backend.URL, "")

	got := doGet(t, proxyTS.URL+"/checkpoints/latest")
	if got["result"] == nil {
		t.Fatal("result field missing")
	}
	resultMap := got["result"].(map[string]interface{})
	if resultMap["root_hash"] != "AAAA==" {
		t.Errorf("root_hash was modified: %v", resultMap["root_hash"])
	}
}

// TestProxy_SetField replaces a specific JSON field in a Heimdall checkpoint response.
func TestProxy_SetField(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"height":"100","result":{"root_hash":"AAAA==","start_block":"0"}}`))
	}))

	rules := `
- name: corrupt-checkpoint-hash
  path_pattern: "/checkpoints/"
  probability: 1.0
  operations:
    - type: set_field
      field: result.root_hash
      value: "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ=="
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	got := doGet(t, proxyTS.URL+"/checkpoints/latest")
	resultMap := got["result"].(map[string]interface{})
	if resultMap["root_hash"] != "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ==" {
		t.Errorf("root_hash not corrupted: %v", resultMap["root_hash"])
	}

	stats := proxy.Stats()
	if stats.RequestsCorrupted != 1 {
		t.Errorf("expected 1 corrupted request, got %d", stats.RequestsCorrupted)
	}
}

// TestProxy_TruncateArray empties the selected_producers array.
func TestProxy_TruncateArray(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"span_id":1,"selected_producers":[{"id":1},{"id":2}]}}`))
	}))

	rules := `
- name: empty-producers
  path_pattern: "/bor/spans/"
  operations:
    - type: truncate_array
      field: result.selected_producers
      value: 0
`
	proxyTS, _ := startProxy(t, backend.URL, rules)

	got := doGet(t, proxyTS.URL+"/bor/spans/latest")
	resultMap := got["result"].(map[string]interface{})
	arr, ok := resultMap["selected_producers"].([]interface{})
	if !ok {
		t.Fatalf("selected_producers is not an array: %T", resultMap["selected_producers"])
	}
	if len(arr) != 0 {
		t.Errorf("selected_producers not truncated: %v", arr)
	}
}

// TestProxy_InjectError replaces the entire response with a JSON-RPC error.
func TestProxy_InjectError(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":"0x1a2"}`))
	}))

	rules := `
- name: inject-author-error
  path_pattern: "/"
  method_match: "bor_getAuthor"
  operations:
    - type: inject_error
`
	proxyTS, _ := startProxy(t, backend.URL, rules)

	payload := `{"jsonrpc":"2.0","id":7,"method":"bor_getAuthor","params":["latest"]}`
	got := doPost(t, proxyTS.URL+"/", payload)

	if _, hasErr := got["error"]; !hasErr {
		t.Fatalf("expected error object in response, got: %v", got)
	}
	if _, hasResult := got["result"]; hasResult {
		t.Error("response must not have 'result' when error is injected")
	}
}

// TestProxy_MethodMatch_NoMatch verifies that a rule with method_match does NOT
// fire for requests with a different JSON-RPC method.
func TestProxy_MethodMatch_NoMatch(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x42"}`))
	}))

	rules := `
- name: corrupt-block-number
  path_pattern: "/"
  method_match: "eth_blockNumber"
  operations:
    - type: set_field
      field: result
      value: "0x0"
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	// Send a different method — should NOT be corrupted.
	payload := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
	got := doPost(t, proxyTS.URL+"/", payload)

	if got["result"] != "0x42" {
		t.Errorf("eth_getBalance result was unexpectedly mutated: %v", got["result"])
	}
	stats := proxy.Stats()
	if stats.RequestsCorrupted != 0 {
		t.Errorf("expected 0 corruptions for non-matching method, got %d", stats.RequestsCorrupted)
	}
}

// TestProxy_Probability_Zero ensures probability=0 never corrupts.
func TestProxy_Probability_Zero(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"original"}`))
	}))

	rules := `
- name: never-fires
  path_pattern: "/"
  probability: 0.0
  operations:
    - type: set_field
      field: result
      value: "corrupted"
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	for i := 0; i < 10; i++ {
		got := doGet(t, proxyTS.URL+"/")
		if got["result"] != "original" {
			t.Fatalf("request %d was corrupted despite probability=0", i)
		}
	}
	stats := proxy.Stats()
	if stats.RequestsCorrupted != 0 {
		t.Errorf("expected 0 corruptions, got %d", stats.RequestsCorrupted)
	}
}

// TestProxy_NonJSONPassThrough verifies that non-JSON bodies are forwarded unmodified.
func TestProxy_NonJSONPassThrough(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not json"))
	}))

	rules := `
- name: would-corrupt
  path_pattern: "/"
  operations:
    - type: set_field
      field: result
      value: "corrupted"
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	resp, err := http.Get(proxyTS.URL + "/") //nolint:noctx
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if string(body) != "not json" {
		t.Errorf("non-JSON body was modified: %q", body)
	}
	stats := proxy.Stats()
	if stats.RequestsCorrupted != 0 {
		t.Errorf("expected 0 corruptions for non-JSON, got %d", stats.RequestsCorrupted)
	}
}

// TestProxy_StatefulSkipFirst verifies that the first N requests are skipped.
func TestProxy_StatefulSkipFirst(t *testing.T) {
	calls := 0
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"original"}`))
	}))

	rules := `
- name: skip-two-then-corrupt
  path_pattern: "/"
  probability: 1.0
  operations:
    - type: set_field
      field: result
      value: "corrupted"
  stateful:
    skip_first: 2
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	// First two should pass through.
	for i := 0; i < 2; i++ {
		got := doGet(t, proxyTS.URL+"/")
		if got["result"] != "original" {
			t.Errorf("request %d (skip window) was corrupted unexpectedly", i)
		}
	}
	// Third should be corrupted.
	got := doGet(t, proxyTS.URL+"/")
	if got["result"] != "corrupted" {
		t.Errorf("request 3 (after skip) was not corrupted: %v", got["result"])
	}

	stats := proxy.Stats()
	if stats.RequestsCorrupted != 1 {
		t.Errorf("expected 1 corruption, got %d", stats.RequestsCorrupted)
	}
	_ = calls // backend was hit for all 3 requests
	_ = proxy
}

// TestProxy_Stats verifies the stats endpoint returns correct counts after
// a mix of pass-through and corrupted requests.
func TestProxy_Stats(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"val"}`))
	}))

	rules := `
- name: corrupt-all
  path_pattern: "/corrupt"
  probability: 1.0
  operations:
    - type: set_field
      field: result
      value: "bad"
`
	proxyTS, proxy := startProxy(t, backend.URL, rules)

	doGet(t, proxyTS.URL+"/corrupt")
	doGet(t, proxyTS.URL+"/corrupt")
	doGet(t, proxyTS.URL+"/passthru")

	stats := proxy.Stats()
	if stats.RequestsTotal != 3 {
		t.Errorf("total=%d, want 3", stats.RequestsTotal)
	}
	if stats.RequestsCorrupted != 2 {
		t.Errorf("corrupted=%d, want 2", stats.RequestsCorrupted)
	}
	if stats.RequestsPassThru != 1 {
		t.Errorf("passthru=%d, want 1", stats.RequestsPassThru)
	}
}

// TestProxy_ConcurrentRequests exercises the proxy under concurrent load to
// verify there are no data races. Run with: go test -race ./...
func TestProxy_ConcurrentRequests(t *testing.T) {
	backend := startFakeBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"hash":"0xabc","producers":[{"id":1},{"id":2}]}}`))
	}))

	rules := `
- name: concurrent-corrupt
  path_pattern: "/"
  probability: 0.5
  operations:
    - type: set_field
      field: result.hash
      value: "0x000"
    - type: truncate_array
      field: result.producers
      value: 1
`
	proxyTS, _ := startProxy(t, backend.URL, rules)

	const workers = 20
	const reqsPerWorker = 10
	done := make(chan struct{}, workers)

	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			client := &http.Client{}
			for i := 0; i < reqsPerWorker; i++ {
				resp, err := client.Get(proxyTS.URL + "/")
				if err != nil {
					continue
				}
				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	// No assertions needed — the race detector catches any data races.
}
