package corruption

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// unmarshal is a helper that parses raw JSON into map[string]interface{}.
func unmarshal(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// get is a convenience accessor for nested dot-path values in tests.
func get(t *testing.T, obj map[string]interface{}, path string) interface{} {
	t.Helper()
	v, err := getField(obj, path)
	if err != nil {
		t.Fatalf("get(%q): %v", path, err)
	}
	return v
}

// ─────────────────────────────────────────────────────────────────────────────
// SetField
// ─────────────────────────────────────────────────────────────────────────────

func TestSetField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		path     string
		value    interface{}
		wantPath string
		want     interface{}
	}{
		{
			name:     "top-level string",
			input:    `{"foo":"bar"}`,
			path:     "foo",
			value:    "baz",
			wantPath: "foo",
			want:     "baz",
		},
		{
			name:     "nested path creates parent",
			input:    `{}`,
			path:     "result.hash",
			value:    "0xdeadbeef",
			wantPath: "result.hash",
			want:     "0xdeadbeef",
		},
		{
			name:     "set nested field in heimdall checkpoint",
			input:    `{"result":{"root_hash":"AAAA==","start_block":"100"}}`,
			path:     "result.root_hash",
			value:    "XXXXXXXX==",
			wantPath: "result.root_hash",
			want:     "XXXXXXXX==",
		},
		{
			name:     "set selected_producers to empty array",
			input:    `{"result":{"span_id":1,"selected_producers":[{"id":1},{"id":2}]}}`,
			path:     "result.selected_producers",
			value:    []interface{}{},
			wantPath: "result.selected_producers",
			want:     []interface{}{},
		},
		{
			name:     "overwrite bor_chain_id",
			input:    `{"result":{"bor_chain_id":"137"}}`,
			path:     "result.bor_chain_id",
			value:    "99999",
			wantPath: "result.bor_chain_id",
			want:     "99999",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := unmarshal(t, tc.input)
			if err := SetField(obj, tc.path, tc.value); err != nil {
				t.Fatalf("SetField(%q): %v", tc.path, err)
			}
			got := get(t, obj, tc.wantPath)
			// Compare via JSON round-trip to handle []interface{} equality.
			wantJSON, _ := json.Marshal(tc.want)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestSetField_NonObjectParent(t *testing.T) {
	obj := unmarshal(t, `{"result":"not-an-object"}`)
	err := SetField(obj, "result.hash", "0x00")
	if err == nil {
		t.Fatal("expected error when descending into non-object, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteField
// ─────────────────────────────────────────────────────────────────────────────

func TestDeleteField(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		path         string
		absentAfter  string
	}{
		{
			name:        "top-level field",
			input:       `{"root_hash":"AAA=","start_block":"0"}`,
			path:        "root_hash",
			absentAfter: "root_hash",
		},
		{
			name:        "nested field in checkpoint response",
			input:       `{"result":{"root_hash":"AAA=","start_block":"0"}}`,
			path:        "result.root_hash",
			absentAfter: "result.root_hash",
		},
		{
			name:        "absent path is a no-op",
			input:       `{"result":{}}`,
			path:        "result.missing",
			absentAfter: "result.missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := unmarshal(t, tc.input)
			DeleteField(obj, tc.path)
			_, err := getField(obj, tc.absentAfter)
			if err == nil {
				t.Errorf("field %q still present after delete", tc.absentAfter)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CorruptBase64
// ─────────────────────────────────────────────────────────────────────────────

func TestCorruptBase64(t *testing.T) {
	// Encode a known byte slice so we can verify the XOR flip.
	original := []byte("heimdall_checkpoint_root_hash_data")
	encoded := base64.StdEncoding.EncodeToString(original)

	obj := map[string]interface{}{
		"result": map[string]interface{}{
			"root_hash": encoded,
		},
	}

	if err := CorruptBase64(obj, "result.root_hash"); err != nil {
		t.Fatalf("CorruptBase64: %v", err)
	}

	corrupted, ok := get(t, obj, "result.root_hash").(string)
	if !ok {
		t.Fatal("field is not a string after CorruptBase64")
	}
	if corrupted == encoded {
		t.Error("field was not modified by CorruptBase64")
	}

	// Decode and verify every byte was XOR'd with 0x01.
	data, err := base64.StdEncoding.DecodeString(corrupted)
	if err != nil {
		t.Fatalf("corrupted value is not valid base64: %v", err)
	}
	for i, b := range data {
		if b != original[i]^0x01 {
			t.Errorf("byte[%d]: got 0x%02x, want 0x%02x", i, b, original[i]^0x01)
		}
	}
}

func TestCorruptBase64_NotBase64(t *testing.T) {
	obj := map[string]interface{}{"hash": "0xdeadbeef_not_base64!!!"}
	err := CorruptBase64(obj, "hash")
	if err == nil {
		t.Fatal("expected error for non-base64 field, got nil")
	}
}

func TestCorruptBase64_Empty(t *testing.T) {
	obj := map[string]interface{}{"hash": ""}
	// Empty string is valid base64 (zero bytes); should not error.
	if err := CorruptBase64(obj, "hash"); err != nil {
		t.Fatalf("unexpected error for empty field: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TruncateArray
// ─────────────────────────────────────────────────────────────────────────────

func TestTruncateArray(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		path    string
		n       int
		wantLen int
	}{
		{
			name:    "truncate to 0 clears array",
			input:   `{"result":{"event_records":[{"id":1},{"id":2},{"id":3}]}}`,
			path:    "result.event_records",
			n:       0,
			wantLen: 0,
		},
		{
			name:    "truncate to 1",
			input:   `{"result":{"event_records":[{"id":1},{"id":2},{"id":3}]}}`,
			path:    "result.event_records",
			n:       1,
			wantLen: 1,
		},
		{
			name:    "truncate to more than length keeps all",
			input:   `{"result":{"event_records":[{"id":1}]}}`,
			path:    "result.event_records",
			n:       10,
			wantLen: 1,
		},
		{
			name:    "negative n clears array",
			input:   `{"result":{"selected_producers":[{"id":1},{"id":2}]}}`,
			path:    "result.selected_producers",
			n:       -1,
			wantLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := unmarshal(t, tc.input)
			if err := TruncateArray(obj, tc.path, tc.n); err != nil {
				t.Fatalf("TruncateArray: %v", err)
			}
			arr, ok := get(t, obj, tc.path).([]interface{})
			if !ok {
				t.Fatalf("field is not an array after truncate")
			}
			if len(arr) != tc.wantLen {
				t.Errorf("got len=%d, want len=%d", len(arr), tc.wantLen)
			}
		})
	}
}

func TestTruncateArray_NotArray(t *testing.T) {
	obj := unmarshal(t, `{"result":{"event_records":"not-an-array"}}`)
	err := TruncateArray(obj, "result.event_records", 0)
	if err == nil {
		t.Fatal("expected error for non-array field, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// InjectError
// ─────────────────────────────────────────────────────────────────────────────

func TestInjectError(t *testing.T) {
	tests := []struct {
		name       string
		originalID interface{}
		wantIDJSON string
	}{
		{"integer id", float64(42), "42"},
		{"string id", "req-1", `"req-1"`},
		{"null id", nil, "null"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := InjectError(tc.originalID)

			// Must be a valid JSON-RPC 2.0 error.
			if result["jsonrpc"] != "2.0" {
				t.Errorf("jsonrpc = %v, want 2.0", result["jsonrpc"])
			}
			errObj, ok := result["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("error field is missing or not an object")
			}
			code, ok := errObj["code"].(int)
			if !ok || code != -32000 {
				t.Errorf("error.code = %v (%T), want -32000", errObj["code"], errObj["code"])
			}
			if errObj["message"] == "" {
				t.Error("error.message is empty")
			}

			// Verify id round-trips correctly.
			idJSON, _ := json.Marshal(result["id"])
			if string(idJSON) != tc.wantIDJSON {
				t.Errorf("id JSON = %s, want %s", idJSON, tc.wantIDJSON)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ApplyOp – integration over all operation types
// ─────────────────────────────────────────────────────────────────────────────

func TestApplyOp_SetField(t *testing.T) {
	// Realistic Heimdall span response
	obj := unmarshal(t, `{
		"result": {
			"span_id": 1,
			"bor_chain_id": "137",
			"selected_producers": [{"id":1,"address":"0xaaa"},{"id":2,"address":"0xbbb"}]
		}
	}`)

	op := CorruptionOp{Type: "set_field", Field: "result.bor_chain_id", Value: "99999"}
	repl, err := ApplyOp(obj, op, nil)
	if err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if repl != nil {
		t.Error("set_field should not return a replacement object")
	}
	if get(t, obj, "result.bor_chain_id") != "99999" {
		t.Errorf("bor_chain_id was not updated")
	}
}

func TestApplyOp_InjectError_ReplacesObject(t *testing.T) {
	obj := unmarshal(t, `{"jsonrpc":"2.0","id":1,"result":"0x1a2"}`)

	op := CorruptionOp{Type: "inject_error"}
	repl, err := ApplyOp(obj, op, float64(1))
	if err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if repl == nil {
		t.Fatal("inject_error must return a replacement object")
	}
	if _, hasErr := repl["error"]; !hasErr {
		t.Error("replacement object missing 'error' field")
	}
	if _, hasResult := repl["result"]; hasResult {
		t.Error("replacement object must not have 'result' field")
	}
}

func TestApplyOp_UnknownType(t *testing.T) {
	obj := unmarshal(t, `{"foo":"bar"}`)
	op := CorruptionOp{Type: "nonexistent_op", Field: "foo"}
	_, err := ApplyOp(obj, op, nil)
	if err == nil {
		t.Fatal("expected error for unknown operation type, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Realistic Polygon JSON payloads
// ─────────────────────────────────────────────────────────────────────────────

// TestRealisticHeimdallSpan verifies the corruption sequence that empties
// selected_producers in a realistic Heimdall span response.
func TestRealisticHeimdallSpan(t *testing.T) {
	spanJSON := `{
		"height": "12345",
		"result": {
			"span_id": 2,
			"start_block": 6400,
			"end_block": 12800,
			"bor_chain_id": "137",
			"validator_set": {"validators":[{"id":1,"address":"0xabc","voting_power":10000}]},
			"selected_producers": [
				{"id":1,"address":"0xabc","voting_power":10000},
				{"id":2,"address":"0xdef","voting_power":8000}
			]
		}
	}`
	obj := unmarshal(t, spanJSON)

	// Simulate span_empty_producers rule.
	op := CorruptionOp{Type: "set_field", Field: "result.selected_producers", Value: []interface{}{}}
	if _, err := ApplyOp(obj, op, nil); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	arr, ok := get(t, obj, "result.selected_producers").([]interface{})
	if !ok || len(arr) != 0 {
		t.Errorf("selected_producers not cleared: %v", get(t, obj, "result.selected_producers"))
	}
	// Other fields must be untouched.
	if get(t, obj, "result.bor_chain_id") != "137" {
		t.Error("bor_chain_id was unexpectedly modified")
	}
}

// TestRealisticBorBlockHash verifies hash corruption on a realistic eth_getHeaderByNumber response.
func TestRealisticBorBlockHash(t *testing.T) {
	blockJSON := `{
		"jsonrpc": "2.0",
		"id": 1,
		"result": {
			"number": "0x1a2b3",
			"hash": "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"parentHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
			"miner": "0xde0B295669a9FD93d5F28D9Ec85E40f4cb697BAe"
		}
	}`
	obj := unmarshal(t, blockJSON)
	original := get(t, obj, "result.hash").(string)

	op := CorruptionOp{Type: "set_field", Field: "result.hash", Value: "0x0000000000000000000000000000000000000000000000000000000000000000"}
	if _, err := ApplyOp(obj, op, nil); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	corrupted := get(t, obj, "result.hash").(string)
	if corrupted == original {
		t.Error("hash was not changed")
	}
	if corrupted != "0x0000000000000000000000000000000000000000000000000000000000000000" {
		t.Errorf("unexpected hash: %s", corrupted)
	}
}

// TestRealisticStateSyncTruncate verifies state sync event truncation.
func TestRealisticStateSyncTruncate(t *testing.T) {
	syncJSON := `{
		"height": "99000",
		"result": {
			"event_records": [
				{"id":1,"contract":"0xaaa","data":"AAEC","tx_hash":"0x111","log_index":0,"chain_id":"137"},
				{"id":2,"contract":"0xbbb","data":"BAAB","tx_hash":"0x222","log_index":1,"chain_id":"137"},
				{"id":3,"contract":"0xccc","data":"CABC","tx_hash":"0x333","log_index":0,"chain_id":"137"}
			]
		}
	}`
	obj := unmarshal(t, syncJSON)

	op := CorruptionOp{Type: "truncate_array", Field: "result.event_records", Value: 0}
	if _, err := ApplyOp(obj, op, nil); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	arr := get(t, obj, "result.event_records").([]interface{})
	if len(arr) != 0 {
		t.Errorf("expected empty array, got %d elements", len(arr))
	}
}

// TestRealisticCheckpointHash verifies root_hash mutation on a Heimdall checkpoint.
func TestRealisticCheckpointHash(t *testing.T) {
	cpJSON := `{
		"height": "456",
		"result": {
			"proposer": "0xde0B295669a9FD93d5F28D9Ec85E40f4cb697BAe",
			"start_block": "100",
			"end_block": "255",
			"root_hash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			"account_root_hash": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
			"bor_chain_id": "137"
		}
	}`
	obj := unmarshal(t, cpJSON)
	badHash := "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ=="

	op := CorruptionOp{Type: "set_field", Field: "result.root_hash", Value: badHash}
	if _, err := ApplyOp(obj, op, nil); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	got := get(t, obj, "result.root_hash").(string)
	if got != badHash {
		t.Errorf("root_hash = %q, want %q", got, badHash)
	}
	// start_block must be unchanged.
	if get(t, obj, "result.start_block") != "100" {
		t.Error("start_block was unexpectedly modified")
	}
}
