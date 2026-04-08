package bor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// adminNodeInfo is the shape returned by Bor's admin_nodeInfo JSON-RPC method.
// We only decode the fields we need; the full struct has many more fields.
type adminNodeInfo struct {
	Enode string `json:"enode"`
	// Ports and ListenAddr are provided for informational logging.
	Ports struct {
		Discovery int `json:"discovery"`
		Listener  int `json:"listener"`
	} `json:"ports"`
	ListenAddr string `json:"listenAddr"`
}

// DiscoverEnodes queries admin_nodeInfo on each provided Bor RPC URL and
// returns the discovered enode URLs. It is tolerant of individual failures:
// if a particular RPC endpoint is unreachable or returns an error, that entry
// is skipped and the error is logged to the returned error slice.
//
// Returns a non-nil error only when every URL fails.
func DiscoverEnodes(ctx context.Context, rpcURLs []string) ([]string, error) {
	if len(rpcURLs) == 0 {
		return nil, fmt.Errorf("no RPC URLs provided")
	}

	enodes := make([]string, 0, len(rpcURLs))
	var lastErr error

	for _, url := range rpcURLs {
		enode, err := queryNodeInfo(ctx, url)
		if err != nil {
			lastErr = fmt.Errorf("admin_nodeInfo from %s: %w", url, err)
			continue
		}
		enodes = append(enodes, enode)
	}

	if len(enodes) == 0 {
		return nil, fmt.Errorf("all admin_nodeInfo queries failed; last error: %w", lastErr)
	}
	return enodes, nil
}

// DiscoverSingleEnode queries admin_nodeInfo on a single RPC URL and returns
// the enode URL. Returns an error if the call fails or the response has no enode.
func DiscoverSingleEnode(ctx context.Context, rpcURL string) (string, error) {
	return queryNodeInfo(ctx, rpcURL)
}

// queryNodeInfo performs a single admin_nodeInfo JSON-RPC call and returns
// the enode URL from the result.
func queryNodeInfo(ctx context.Context, rpcURL string) (string, error) {
	reqBody := `{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}`

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewBufferString(reqBody))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64 KiB limit
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, rpcURL, truncate(string(body), 256))
	}

	// Parse the JSON-RPC envelope.
	var envelope struct {
		Result adminNodeInfo `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(body), 256))
	}
	if envelope.Error != nil {
		return "", fmt.Errorf("JSON-RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result.Enode == "" {
		return "", fmt.Errorf("empty enode in admin_nodeInfo response from %s", rpcURL)
	}

	// admin_nodeInfo sometimes returns the enode with an unspecified IP (0.0.0.0
	// or [::]). If so, derive the host from the RPC URL.
	enode := fixEnodeIP(envelope.Result.Enode, rpcURL)
	return enode, nil
}

// fixEnodeIP replaces a 0.0.0.0 or [::] listen address in an enode URL with
// the host from the RPC URL. This is common when Bor binds on all interfaces
// and admin_nodeInfo reports the unspecified address.
func fixEnodeIP(enodeURL, rpcURL string) string {
	needsFix := strings.Contains(enodeURL, "@0.0.0.0") ||
		strings.Contains(enodeURL, "@[::]") ||
		strings.Contains(enodeURL, "@127.0.0.1")
	if !needsFix {
		return enodeURL
	}

	// Extract host from rpc URL.
	// rpcURL is typically "http://host:port" or "http://host:port/..."
	host := rpcURL
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	// Drop path.
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	// Drop port.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}

	enodeURL = strings.ReplaceAll(enodeURL, "@0.0.0.0", "@"+host)
	enodeURL = strings.ReplaceAll(enodeURL, "@[::]", "@"+host)
	enodeURL = strings.ReplaceAll(enodeURL, "@127.0.0.1", "@"+host)
	return enodeURL
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
