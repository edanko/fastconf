package value

import (
	"math"
	"reflect"
	"testing"
)

func TestFromKinds(t *testing.T) {
	tests := []struct {
		name string
		in   any
		kind Kind
	}{
		{"nil", nil, KindNull},
		{"bool", true, KindBool},
		{"int", 42, KindInt},
		{"int64", int64(42), KindInt},
		{"uint", uint(42), KindInt},
		{"float64", 1.5, KindFloat},
		{"string", "x", KindString},
		{"list", []any{1, "two"}, KindList},
		{"object", map[string]any{"a": 1}, KindObject},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			converted := From(test.in)
			if converted.Kind() != test.kind {
				t.Fatalf("kind = %v, want %v", converted.Kind(), test.kind)
			}
		})
	}
}

func TestFromUnsignedOverflow(t *testing.T) {
	converted := From(uint64(math.MaxUint64))
	if converted.Kind() != KindFloat {
		t.Fatalf("kind = %v, want float for overflowing uint64", converted.Kind())
	}
}

func TestObjectLookup(t *testing.T) {
	object := FromMap(map[string]any{
		"Database": map[string]any{"Host": "localhost"},
		"port":     5432,
	})
	value, ok := object.Get("Database")
	if !ok {
		t.Fatal("exact lookup failed")
	}
	database := value.Object()
	if _, ok := database.Get("host"); !ok {
		t.Fatal("case-insensitive lookup failed")
	}
	if got, ok := database.GetFold("HOST"); !ok || got.Kind() != KindString {
		t.Fatalf("GetFold = %v, %v", got.Kind(), ok)
	}
	if _, ok := object.Get("missing"); ok {
		t.Fatal("unexpected lookup success")
	}
}

func TestInterfaceDeepCopy(t *testing.T) {
	source := From(map[string]any{
		"object": map[string]any{"list": []any{"a", "b"}},
	})
	plain := source.Interface().(map[string]any)
	plain["object"].(map[string]any)["list"].([]any)[0] = "mutated"
	second := source.Interface().(map[string]any)
	if second["object"].(map[string]any)["list"].([]any)[0] != "a" {
		t.Fatal("Interface returned shared memory")
	}
}

func TestInterfaceRendering(t *testing.T) {
	converted := From(map[string]any{"a": 1, "b": []any{true, "x"}})
	want := map[string]any{"a": int64(1), "b": []any{true, "x"}}
	if got := converted.Interface(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Interface = %#v, want %#v", got, want)
	}
}
