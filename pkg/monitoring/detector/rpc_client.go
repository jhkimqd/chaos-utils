package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// rpcClient is a minimal JSON-RPC 2.0 HTTP client for EVM nodes.
type rpcClient struct {
	url    string
	client *http.Client
}

func newRPCClient(url string) *rpcClient {
	return &rpcClient{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// EthCall executes eth_call with the given "to" address and calldata at the latest block.
// Returns the hex-encoded return value (e.g., "0xdeadbeef") or "0x" for empty.
func (c *rpcClient) EthCall(ctx context.Context, to, data string) (string, error) {
	params := []interface{}{
		map[string]string{
			"to":   to,
			"data": data,
		},
		"latest",
	}
	return c.call(ctx, "eth_call", params)
}

func (c *rpcClient) call(ctx context.Context, method string, params []interface{}) (string, error) {
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return "", fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Unwrap JSON string (e.g. `"0xdeadbeef"`) to a bare hex string.
	if len(rpcResp.Result) >= 2 && rpcResp.Result[0] == '"' {
		var s string
		if err := json.Unmarshal(rpcResp.Result, &s); err != nil {
			return "", fmt.Errorf("unmarshal result string: %w", err)
		}
		return s, nil
	}
	return string(rpcResp.Result), nil
}
