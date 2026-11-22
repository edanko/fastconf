package encoding

import (
	"io"
	"strings"
	"testing"
)

// TestRegisterNormalizesCustomDecoder verifies that decoders installed through
// Register are still normalized by Decode, even though the built-in JSON
// decoder skips normalization because encoding/json is already canonical.
func TestRegisterNormalizesCustomDecoder(t *testing.T) {
	const custom Encoding = "custom-test"
	Register(custom, func(io.Reader) (map[string]any, error) {
		return map[string]any{
			"nested": map[any]any{1: "one"},
			"list":   []map[string]any{{"key": "value"}},
		}, nil
	})
	t.Cleanup(func() {
		registryMutex.Lock()
		delete(registry, custom)
		registryMutex.Unlock()
	})

	document, err := Decode(strings.NewReader(""), custom)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	nested, ok := document["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested = %#v, want map[string]any", document["nested"])
	}
	if nested["1"] != "one" {
		t.Fatalf("nested = %#v", nested)
	}
	list, ok := document["list"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("list = %#v, want []any", document["list"])
	}
	if object, ok := list[0].(map[string]any); !ok || object["key"] != "value" {
		t.Fatalf("list[0] = %#v", list[0])
	}
}
