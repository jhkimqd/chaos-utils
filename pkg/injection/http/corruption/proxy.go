package corruption

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maxBodyBytes is the upper bound for response bodies that the proxy will
	// read into memory for mutation. Responses larger than this are forwarded
	// unmodified to avoid unbounded memory growth.
	maxBodyBytes = 10 * 1024 * 1024 // 10 MiB

	// contextKeyRequestMeta is the context key used to pass per-request metadata
	// from the Director into ModifyResponse, avoiding mutable shared state.
	contextKeyRequestMeta = contextKey("request_meta")
)

// contextKey is a private type to avoid collisions in the context value map.
type contextKey string

// requestMeta captures request-time data that ModifyResponse needs but cannot
// read from the response (the request body may have been consumed by then).
type requestMeta struct {
	path   string
	method string // JSON-RPC method field, empty for non-JSON-RPC requests
}

// CorruptionProxy is an HTTP reverse proxy that applies CorruptionRules to
// matching responses. It is safe for concurrent use.
type CorruptionProxy struct {
	rp      *httputil.ReverseProxy
	rules   *RuleSet
	stats   *proxyStats
	rng     *lockedRand
}

// proxyStats tracks aggregate counters for the GET /stats control endpoint.
type proxyStats struct {
	requestsTotal    atomic.Int64
	requestsCorrupted atomic.Int64
	requestsPassThru atomic.Int64
}

// NewProxy constructs a CorruptionProxy that forwards to targetURL and applies
// rules from the supplied RuleSet.
func NewProxy(targetURL *url.URL, rules *RuleSet) *CorruptionProxy {
	cp := &CorruptionProxy{
		rules: rules,
		stats: &proxyStats{},
		rng:   newLockedRand(time.Now().UnixNano()),
	}

	rp := &httputil.ReverseProxy{
		Director:       cp.director(targetURL),
		ModifyResponse: cp.modifyResponse,
		ErrorHandler:   cp.errorHandler,
		// Use a generous buffer pool to reduce allocations under load.
		BufferPool: newBufferPool(),
	}
	cp.rp = rp
	return cp
}

// ServeHTTP satisfies http.Handler so CorruptionProxy can be used directly
// with http.ListenAndServe.
func (cp *CorruptionProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cp.stats.requestsTotal.Add(1)
	cp.rp.ServeHTTP(w, r)
}

// director builds the ReverseProxy Director function. It:
//  1. Rewrites the URL to target the upstream service.
//  2. Reads the request body (buffered) to extract the JSON-RPC method field
//     when the Content-Type is application/json.
//  3. Stores path + method in request context for ModifyResponse.
//
// Buffering the request body here is necessary because http.Request.Body is a
// one-shot reader. We restore it with io.NopCloser so the upstream still
// receives the full body.
func (cp *CorruptionProxy) director(target *url.URL) func(*http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		// Preserve the original path and query.
		if target.Path != "" {
			req.URL.Path = target.Path + req.URL.Path
		}

		meta := &requestMeta{path: req.URL.Path}

		// Extract JSON-RPC method from request body without consuming it.
		if isJSONRPC(req) {
			if method, body, err := extractJSONRPCMethod(req.Body); err == nil {
				meta.method = method
				req.Body = io.NopCloser(bytes.NewReader(body))
			} else {
				// Body unreadable — restore an empty reader so upstream still works.
				req.Body = http.NoBody
			}
		}

		// Attach metadata to context for ModifyResponse.
		ctx := context.WithValue(req.Context(), contextKeyRequestMeta, meta)
		*req = *req.WithContext(ctx)
	}
}

// modifyResponse intercepts the upstream response and applies corruption rules.
// If the response body is > maxBodyBytes it is passed through unmodified.
// If the body is not valid JSON it is also passed through unmodified.
func (cp *CorruptionProxy) modifyResponse(resp *http.Response) error {
	meta, _ := resp.Request.Context().Value(contextKeyRequestMeta).(*requestMeta)
	if meta == nil {
		meta = &requestMeta{path: resp.Request.URL.Path}
	}

	// Find an applicable rule, if any.
	rs := cp.matchRule(meta)
	if rs == nil {
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	seen := rs.seen.Add(1)

	// Apply stateful gate (skip_first / every_n / max_corrupted) before
	// probability to keep the probability sampling independent of counters.
	if !cp.shouldCorruptStateful(rs, seen) {
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	// Probability gate.
	if cp.rng.float64() >= rs.rule.effectiveProbability() {
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	// Read and buffer the response body up to maxBodyBytes.
	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		// Can't read body — pass through.
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		cp.stats.requestsPassThru.Add(1)
		return nil
	}
	_ = resp.Body.Close()

	if len(buf) > maxBodyBytes {
		// Response is too large — pass through unmodified.
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	// Parse JSON. If body is not valid JSON, pass through unmodified.
	var obj map[string]interface{}
	if err := json.Unmarshal(buf, &obj); err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	// Extract original JSON-RPC id so inject_error can echo it back.
	var originalID interface{}
	if id, ok := obj["id"]; ok {
		originalID = id
	}

	// Apply all operations in order. A single inject_error replaces the whole
	// object and short-circuits remaining ops.
	current := obj
	for _, op := range rs.rule.Operations {
		replacement, err := ApplyOp(current, op, originalID)
		if err != nil {
			// Log and skip this op rather than aborting the whole request.
			fmt.Printf("[CORRUPT] rule=%q op=%q error: %v\n", rs.rule.Name, op.Type, err)
			continue
		}
		if replacement != nil {
			current = replacement
			break // inject_error terminates operation chain
		}
	}

	// Re-serialise.
	out, err := json.Marshal(current)
	if err != nil {
		// Marshal failure — pass through original body.
		resp.Body = io.NopCloser(bytes.NewReader(buf))
		cp.stats.requestsPassThru.Add(1)
		return nil
	}

	rs.corrupted.Add(1)
	cp.stats.requestsCorrupted.Add(1)

	fmt.Printf("[CORRUPT] rule=%q path=%q method=%q original_len=%d corrupted_len=%d\n",
		rs.rule.Name, meta.path, meta.method, len(buf), len(out))

	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(out)))
	resp.Header.Set("X-Chaos-Corruption", rs.rule.Name)

	return nil
}

// errorHandler logs upstream connection errors without crashing.
func (cp *CorruptionProxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	fmt.Printf("[CORRUPT] upstream error: %v\n", err)
	w.WriteHeader(http.StatusBadGateway)
}

// matchRule returns the first ruleWithState whose path and method patterns
// match the request metadata. Returns nil when no rule matches.
func (cp *CorruptionProxy) matchRule(meta *requestMeta) *ruleWithState {
	cp.rules.mu.RLock()
	states := cp.rules.rules
	cp.rules.mu.RUnlock()

	for _, rs := range states {
		if !rs.matchesPath(meta.path) {
			continue
		}
		if !rs.matchesMethod(meta.method) {
			continue
		}
		return rs
	}
	return nil
}

// shouldCorruptStateful applies the StatefulConfig gates.
// seen must be the value returned by rs.seen.Add(1) for this request, ensuring
// the exact counter value is used without a separate Load that could race.
func (cp *CorruptionProxy) shouldCorruptStateful(rs *ruleWithState, seen int64) bool {
	s := rs.rule.Stateful
	if s == nil {
		return true
	}

	corrupted := rs.corrupted.Load()

	// skip_first: ignore the first N matching requests.
	if s.SkipFirst > 0 && seen <= int64(s.SkipFirst) {
		return false
	}

	// max_corrupted: stop after N corruptions.
	if s.MaxCorrupted > 0 && corrupted >= int64(s.MaxCorrupted) {
		return false
	}

	// corrupt_every_n: only corrupt on multiples of N (after skip_first).
	if s.CorruptEveryN > 1 {
		adjusted := seen - int64(s.SkipFirst)
		return adjusted%int64(s.CorruptEveryN) == 0
	}

	return true
}

// Stats returns a snapshot of aggregate proxy counters.
func (cp *CorruptionProxy) Stats() ProxyStats {
	return ProxyStats{
		RequestsTotal:     cp.stats.requestsTotal.Load(),
		RequestsCorrupted: cp.stats.requestsCorrupted.Load(),
		RequestsPassThru:  cp.stats.requestsPassThru.Load(),
		Rules:             cp.rules.Snapshot(),
	}
}

// ProxyStats is the JSON-serialisable payload returned by GET /stats.
type ProxyStats struct {
	RequestsTotal     int64          `json:"requests_total"`
	RequestsCorrupted int64          `json:"requests_corrupted"`
	RequestsPassThru  int64          `json:"requests_pass_thru"`
	Rules             []RuleSnapshot `json:"rules"`
}

// isJSONRPC returns true when the request looks like a JSON-RPC call.
// We check Content-Type and that the path is "/" (Bor standard).
func isJSONRPC(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return (r.Method == http.MethodPost) &&
		(r.Body != nil) &&
		(ct == "application/json" || ct == "application/json; charset=utf-8" || r.URL.Path == "/")
}

// extractJSONRPCMethod reads the request body, extracts the "method" string
// field, and returns (method, rawBody, nil). The raw body bytes are returned
// so the caller can restore req.Body.
//
// Batch requests (JSON arrays) are handled by extracting the method from the
// first element only. This is sufficient for routing/matching purposes.
func extractJSONRPCMethod(body io.Reader) (method string, raw []byte, err error) {
	raw, err = io.ReadAll(io.LimitReader(body, 64*1024))
	if err != nil {
		return "", nil, err
	}
	payload := bytes.TrimSpace(raw)
	if len(payload) == 0 {
		return "", raw, nil
	}
	// Handle batch requests — extract method from first element.
	if payload[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(payload, &batch) != nil || len(batch) == 0 {
			return "", raw, nil
		}
		payload = []byte(batch[0])
	}
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return "", raw, nil // not JSON-RPC, return raw body for pass-through
	}
	return msg.Method, raw, nil
}

// lockedRand is a goroutine-safe wrapper around rand.Rand that serialises
// access to the PRNG for safe use across concurrent requests.
type lockedRand struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newLockedRand(seed int64) *lockedRand {
	return &lockedRand{rng: rand.New(rand.NewSource(seed))} //nolint:gosec
}

func (lr *lockedRand) float64() float64 {
	lr.mu.Lock()
	v := lr.rng.Float64()
	lr.mu.Unlock()
	return v
}

// bufferPool implements httputil.BufferPool using a sync.Pool of 32 KiB slices.
type bufferPool struct {
	pool sync.Pool
}

func newBufferPool() *bufferPool {
	return &bufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				buf := make([]byte, 32*1024)
				return &buf
			},
		},
	}
}

func (bp *bufferPool) Get() []byte {
	buf := bp.pool.Get().(*[]byte)
	return *buf
}

func (bp *bufferPool) Put(buf []byte) {
	bp.pool.Put(&buf)
}
