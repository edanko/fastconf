package maps

import (
	"reflect"
	"testing"
)

func TestGetSetDelete(t *testing.T) {
	document := map[string]any{}
	if err := Set(document, []string{"database", "host"}, "localhost"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := Get(document, []string{"database", "host"})
	if !ok || got != "localhost" {
		t.Fatalf("Get = %v, %v", got, ok)
	}

	if err := Set(document, []string{"database", "port"}, 5432); err != nil {
		t.Fatalf("Set int: %v", err)
	}
	if _, ok := Get(document, []string{"database"}); !ok {
		t.Fatal("nested object was not created")
	}

	removed, ok := Delete(document, []string{"database", "port"})
	if !ok || removed != 5432 {
		t.Fatalf("Delete = %v, %v", removed, ok)
	}
	if _, ok := Get(document, []string{"database", "port"}); ok {
		t.Fatal("value was not deleted")
	}
}

func TestSetFold(t *testing.T) {
	document := map[string]any{"Database": map[string]any{"Host": "localhost"}}
	if err := SetFold(document, []string{"database", "host"}, "remote"); err != nil {
		t.Fatalf("SetFold: %v", err)
	}
	got, ok := GetFold(document, []string{"DATABASE", "HOST"})
	if !ok || got != "remote" {
		t.Fatalf("GetFold = %v, %v", got, ok)
	}
	// The original key casing is preserved.
	object, _ := Get(document, []string{"Database"})
	if _, ok := object.(map[string]any)["Host"]; !ok {
		t.Fatalf("expected original key casing to be preserved: %#v", document)
	}
}

func TestSetNonObjectError(t *testing.T) {
	document := map[string]any{"database": "not-an-object"}
	if err := Set(document, []string{"database", "host"}, "localhost"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMergeOverridesAndCopies(t *testing.T) {
	destination := map[string]any{
		"server": map[string]any{
			"host": "localhost",
			"tls":  map[string]any{"enabled": false},
		},
		"list": []any{"a"},
	}
	source := map[string]any{
		"server": map[string]any{
			"port": 8080,
			"tls":  map[string]any{"enabled": true},
		},
		"list": []any{"b"},
	}
	Merge(destination, source)

	server := destination["server"].(map[string]any)
	if server["host"] != "localhost" || server["port"] != 8080 {
		t.Fatalf("unexpected server merge: %#v", server)
	}
	if tls := server["tls"].(map[string]any); tls["enabled"] != true {
		t.Fatalf("nested override failed: %#v", tls)
	}
	if !reflect.DeepEqual(destination["list"], []any{"b"}) {
		t.Fatalf("list was not replaced: %#v", destination["list"])
	}

	// Mutating the source must not affect the destination.
	source["server"].(map[string]any)["port"] = 9999
	source["list"].([]any)[0] = "mutated"
	if server["port"] != 8080 {
		t.Fatal("destination shares nested maps with the source")
	}
	if destination["list"].([]any)[0] != "b" {
		t.Fatal("destination shares slices with the source")
	}
}

func TestDeepCopyMap(t *testing.T) {
	original := map[string]any{"a": map[string]any{"b": []any{1, 2}}}
	clone := DeepCopyMap(original)
	clone["a"].(map[string]any)["b"].([]any)[0] = 42
	if original["a"].(map[string]any)["b"].([]any)[0] != 1 {
		t.Fatal("DeepCopyMap returned shared memory")
	}
}

func TestExtractMap(t *testing.T) {
	document := map[string]any{"config": map[string]any{"tenants": map[string]any{"acme": map[string]any{"x": 1}}}}
	tenants, ok := ExtractMap(document, []string{"config", "tenants"})
	if !ok {
		t.Fatal("ExtractMap failed")
	}
	if tenants["acme"] == nil {
		t.Fatalf("unexpected tenants: %#v", tenants)
	}
	if _, ok := Get(document, []string{"config", "tenants"}); ok {
		t.Fatal("ExtractMap did not remove the value")
	}
}

func TestDeepCopyMapNilAndEmpty(t *testing.T) {
	if clone := DeepCopyMap(nil); clone != nil {
		t.Fatalf("DeepCopyMap(nil) = %#v, want nil", clone)
	}
	original := map[string]any{}
	clone := DeepCopyMap(original)
	if clone == nil || len(clone) != 0 {
		t.Fatalf("DeepCopyMap(empty) = %#v, want an empty non-nil map", clone)
	}
	clone["key"] = "value"
	if len(original) != 0 {
		t.Fatal("DeepCopyMap(empty) returned shared memory")
	}
}

func TestDeepCopyNestedLists(t *testing.T) {
	original := map[string]any{
		"servers": []any{
			map[string]any{"host": "a"},
			[]any{"b", map[string]any{"host": "c"}},
		},
	}
	clone := DeepCopyMap(original)
	servers := clone["servers"].([]any)
	servers[0].(map[string]any)["host"] = "mutated"
	servers[1].([]any)[1].(map[string]any)["host"] = "mutated"
	if original["servers"].([]any)[0].(map[string]any)["host"] != "a" {
		t.Fatal("nested object is shared with the source")
	}
	if original["servers"].([]any)[1].([]any)[1].(map[string]any)["host"] != "c" {
		t.Fatal("nested list object is shared with the source")
	}
}

func TestSetFoldAndDeleteFold(t *testing.T) {
	document := map[string]any{
		"Database": map[string]any{"Host": "old"},
	}
	if err := SetFold(document, []string{"database", "host"}, "new"); err != nil {
		t.Fatalf("SetFold: %v", err)
	}
	if err := SetFold(document, []string{"Database", "Port"}, 5432); err != nil {
		t.Fatalf("SetFold new key: %v", err)
	}
	database := document["Database"].(map[string]any)
	if database["Host"] != "new" {
		t.Fatalf("Host = %#v, want replaced value", database["Host"])
	}
	if database["Port"] != 5432 {
		t.Fatalf("Port = %#v", database["Port"])
	}

	value, ok := DeleteFold(document, []string{"DATABASE", "host"})
	if !ok || value != "new" {
		t.Fatalf("DeleteFold = %#v, %v", value, ok)
	}
	if _, exists := database["Host"]; exists {
		t.Fatal("DeleteFold did not remove the key")
	}
	if _, ok := DeleteFold(document, []string{"database", "host"}); ok {
		t.Fatal("DeleteFold reported a missing path as removed")
	}
	if _, ok := DeleteFold(document, []string{"database", "host", "deep"}); ok {
		t.Fatal("DeleteFold walked through a missing path")
	}
}

func TestSetFoldRejectsNonObject(t *testing.T) {
	document := map[string]any{"Database": "scalar"}
	if err := SetFold(document, []string{"database", "host"}, "value"); err == nil {
		t.Fatal("SetFold replaced a scalar intermediate object")
	}
}

func TestSetFoldForceReplacesScalar(t *testing.T) {
	document := map[string]any{"Database": "scalar"}
	if err := SetFoldForce(document, []string{"database", "host"}, "db.example"); err != nil {
		t.Fatalf("SetFoldForce: %v", err)
	}
	database, ok := document["Database"].(map[string]any)
	if !ok {
		t.Fatalf("Database = %#v, want an object", document["Database"])
	}
	if database["host"] != "db.example" {
		t.Fatalf("host = %#v", database["host"])
	}
}
