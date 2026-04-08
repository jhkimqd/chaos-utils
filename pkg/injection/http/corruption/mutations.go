package corruption

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// SetField sets a value at a dot-notation path inside obj.
// Intermediate map nodes are created as map[string]interface{} if absent.
// Example: SetField(obj, "result.hash", "0x00") sets obj["result"]["hash"]="0x00".
func SetField(obj map[string]interface{}, path string, value interface{}) error {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		obj[parts[0]] = value
		return nil
	}

	key, rest := parts[0], parts[1]
	child, exists := obj[key]
	if !exists {
		child = make(map[string]interface{})
		obj[key] = child
	}
	childMap, ok := child.(map[string]interface{})
	if !ok {
		return fmt.Errorf("SetField: %q is not an object (got %T)", key, child)
	}
	return SetField(childMap, rest, value)
}

// DeleteField removes the field at a dot-notation path from obj.
// It is a no-op when any intermediate key is absent.
func DeleteField(obj map[string]interface{}, path string) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		delete(obj, parts[0])
		return
	}

	key, rest := parts[0], parts[1]
	child, exists := obj[key]
	if !exists {
		return
	}
	childMap, ok := child.(map[string]interface{})
	if !ok {
		return
	}
	DeleteField(childMap, rest)
}

// CorruptBase64 decodes the base64 string at path, flips one bit per byte
// (XOR 0x01), then re-encodes it. The field must hold a string value.
//
// Useful for corrupting Cosmos SDK amino-encoded fields that arrive as
// base64 in Heimdall REST responses (e.g. checkpoint root_hash, signature bytes).
func CorruptBase64(obj map[string]interface{}, path string) error {
	raw, err := getField(obj, path)
	if err != nil {
		return fmt.Errorf("CorruptBase64: %w", err)
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("CorruptBase64: field %q is not a string (got %T)", path, raw)
	}

	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Try RawStdEncoding (no padding) before giving up
		data, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return fmt.Errorf("CorruptBase64: field %q is not valid base64: %w", path, err)
		}
	}
	if len(data) == 0 {
		return nil // nothing to corrupt
	}
	for i := range data {
		data[i] ^= 0x01
	}
	return SetField(obj, path, base64.StdEncoding.EncodeToString(data))
}

// TruncateArray sets the array at path to the first n elements.
// When n <= 0 the array is replaced with an empty slice.
// Returns an error if the field is not a JSON array.
func TruncateArray(obj map[string]interface{}, path string, n int) error {
	raw, err := getField(obj, path)
	if err != nil {
		return fmt.Errorf("TruncateArray: %w", err)
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return fmt.Errorf("TruncateArray: field %q is not an array (got %T)", path, raw)
	}
	if n <= 0 {
		arr = []interface{}{}
	} else if n < len(arr) {
		arr = arr[:n]
	}
	// n >= len(arr): keep all elements unchanged
	return SetField(obj, path, arr)
}

// InjectError builds a canonical JSON-RPC 2.0 error response that replaces the
// entire response body. The caller is responsible for re-serialising the returned
// map and updating the Content-Length header.
//
//	{ "jsonrpc": "2.0", "id": <original-id>, "error": { "code": -32000, "message": "chaos injected error" } }
func InjectError(originalID interface{}) map[string]interface{} {
	id := originalID
	if id == nil {
		id = json.RawMessage("null")
	}
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    -32000,
			"message": "chaos injected error",
		},
	}
}

// ApplyOp dispatches a single CorruptionOp against obj.
// The originalID argument is only used for "inject_error" to preserve the
// JSON-RPC request id in the synthetic error response.
// When "inject_error" fires it returns a non-nil replacement object that the
// caller must use instead of obj.
func ApplyOp(obj map[string]interface{}, op CorruptionOp, originalID interface{}) (replacement map[string]interface{}, err error) {
	switch op.Type {
	case "set_field":
		return nil, SetField(obj, op.Field, op.Value)

	case "delete_field":
		DeleteField(obj, op.Field)
		return nil, nil

	case "corrupt_base64":
		return nil, CorruptBase64(obj, op.Field)

	case "truncate_array":
		n := 0
		switch v := op.Value.(type) {
		case int:
			n = v
		case float64:
			n = int(v)
		case nil:
			n = 0
		default:
			return nil, fmt.Errorf("truncate_array: Value must be an integer, got %T", op.Value)
		}
		return nil, TruncateArray(obj, op.Field, n)

	case "inject_error":
		return InjectError(originalID), nil

	default:
		return nil, fmt.Errorf("unknown operation type: %q", op.Type)
	}
}

// getField retrieves the value at a dot-notation path from obj.
// Returns an error if any intermediate node is absent or not an object.
func getField(obj map[string]interface{}, path string) (interface{}, error) {
	parts := strings.SplitN(path, ".", 2)
	val, exists := obj[parts[0]]
	if !exists {
		return nil, fmt.Errorf("field %q not found", parts[0])
	}
	if len(parts) == 1 {
		return val, nil
	}
	childMap, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("field %q is not an object (got %T), cannot descend into %q", parts[0], val, parts[1])
	}
	return getField(childMap, parts[1])
}
