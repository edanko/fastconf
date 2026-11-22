package expr

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestResolveNestedDocument verifies recursive resolution of objects and lists
// in place, including typed results.
func TestResolveNestedDocument(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"name":    "svc",
		"ratio":   0.5,
		"enabled": true,
		"database": map[string]any{
			"host": "localhost",
			"dsn":  "${name}@${database.host}",
		},
		"servers": []any{
			map[string]any{"port": 8080, "url": "http://${database.host}"},
			"${name}",
			float64(7),
		},
		"copy": "${database}",
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}

	database := root["database"].(map[string]any) //nolint:forcetypeassert // shape asserted below
	if got := database["dsn"]; got != "svc@localhost" {
		t.Fatalf("database.dsn = %#v; want %q", got, "svc@localhost")
	}
	servers := root["servers"].([]any)
	first := servers[0].(map[string]any)
	if got := first["url"]; got != "http://localhost" {
		t.Fatalf("servers[0].url = %#v; want %q", got, "http://localhost")
	}
	if servers[1] != "svc" {
		t.Fatalf("servers[1] = %#v; want %q", servers[1], "svc")
	}
	if servers[2] != float64(7) {
		t.Fatalf("servers[2] = %#v; want 7", servers[2])
	}
	if root["ratio"] != 0.5 || root["enabled"] != true || root["name"] != "svc" {
		t.Fatalf("scalar values changed: %#v", root)
	}
	copied, ok := root["copy"].(map[string]any)
	if !ok {
		t.Fatalf("copy = %#v; want an object", root["copy"])
	}
	if !reflect.DeepEqual(copied, database) {
		t.Fatalf("copy = %#v; want the resolved database object %#v", copied, database)
	}
}

// TestResolveSplicesLists verifies array unpack splicing in every position.
func TestResolveSplicesLists(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"name":      "svc",
		"base":      []any{"a", "b"},
		"templated": []any{"${name}-1", "plain"},
		"all":       []any{"x", "*${base}", "${name}", "*${templated}"},
		"whole":     "*${base}",
		"nested":    []any{[]any{"*${base}"}},
		"emptyList": []any{"y", "*${missing:[]}", "z"},
		"objects":   []any{map[string]any{"key": "${name}"}, "*${base}"},
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}

	checkList(t, "all", root["all"], []any{"x", "a", "b", "svc", "svc-1", "plain"})
	checkList(t, "whole", root["whole"], []any{"a", "b"})
	checkList(t, "base", root["base"], []any{"a", "b"})
	checkList(t, "emptyList", root["emptyList"], []any{"y", "z"})

	nested, ok := root["nested"].([]any)
	if !ok || len(nested) != 1 {
		t.Fatalf("nested = %#v; want one element", root["nested"])
	}
	checkList(t, "nested[0]", nested[0], []any{"a", "b"})

	objects, ok := root["objects"].([]any)
	if !ok || len(objects) != 3 {
		t.Fatalf("objects = %#v; want three elements", root["objects"])
	}
	object, ok := objects[0].(map[string]any)
	if !ok || object["key"] != "svc" {
		t.Fatalf("objects[0] = %#v; want an object with key svc", objects[0])
	}
	if objects[1] != "a" || objects[2] != "b" {
		t.Fatalf("objects[1:] = %#v; want [a b]", objects[1:])
	}
}

// TestResolveExpression verifies conditional expressions in a document.
func TestResolveExpression(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"environment": "production",
		"debug":       true,
		"database":    map[string]any{"host": "db.internal"},
		"url":         `#{ ${debug} and ${environment} == "production" ? "debug-db" : "prod-db" }`,
		"feature":     `#{ ${environment} == "development" ? "dev" : ${environment} == "production" ? "prod" : "other" }`,
		"weight":      "#{ 1 < 2 ? 10 : 20 }",
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if root["url"] != "debug-db" {
		t.Fatalf("url = %#v; want %q", root["url"], "debug-db")
	}
	if root["feature"] != "prod" {
		t.Fatalf("feature = %#v; want %q", root["feature"], "prod")
	}
	if root["weight"] != float64(10) {
		t.Fatalf("weight = %#v; want 10", root["weight"])
	}
}

// TestResolveErrors verifies path-qualified error reporting.
func TestResolveErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		root      map[string]any
		wantPath  string
		wantError string
	}{
		{
			name:      "compile error in nested object",
			root:      map[string]any{"database": map[string]any{"host": "${oops"}},
			wantPath:  "database.host",
			wantError: "unclosed placeholder",
		},
		{
			name:      "missing value in nested list object",
			root:      map[string]any{"servers": []any{map[string]any{"port": "${missing}"}}},
			wantPath:  "servers[0].port",
			wantError: "is not defined",
		},
		{
			name:      "unpack of non list",
			root:      map[string]any{"name": "svc", "bad": []any{"*${name}"}},
			wantPath:  "bad[0]",
			wantError: "array unpack",
		},
		{
			name:      "unpack of nil",
			root:      map[string]any{"value": nil, "bad": []any{"*${value}"}},
			wantPath:  "bad[0]",
			wantError: "array unpack",
		},
		{
			name:      "unpack after splice keeps final index",
			root:      map[string]any{"name": "svc", "base": []any{"ok"}, "bad": []any{"*${base}", "*${name}"}},
			wantPath:  "bad[1]",
			wantError: "array unpack",
		},
		{
			name:      "unpack syntax error",
			root:      map[string]any{"bad": []any{"*${list}tail"}},
			wantPath:  "bad[0]",
			wantError: "must be the entire value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Resolve(context.Background(), tt.root)
			if err == nil {
				t.Fatalf("Resolve succeeded; want an error containing %q", tt.wantError)
			}
			var pathErr *PathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("Resolve error type = %T; want *PathError (%v)", err, err)
			}
			if !strings.Contains(pathErr.Path, tt.wantPath) {
				t.Fatalf("Resolve error path = %q; want it to contain %q (%v)", pathErr.Path, tt.wantPath, err)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Resolve error = %q; want it to contain %q", err, tt.wantError)
			}
		})
	}
}

// TestResolveCycle verifies cycle detection through the document resolver.
func TestResolveCycle(t *testing.T) {
	t.Parallel()

	root := map[string]any{"a": "${b}", "b": "${a}"}
	err := Resolve(context.Background(), root)
	if err == nil {
		t.Fatal("Resolve succeeded on a placeholder cycle")
	}
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("Resolve error type = %T; want *CycleError (%v)", err, err)
	}
	if len(cycle.Chain) < 3 || cycle.Chain[0] != cycle.Chain[len(cycle.Chain)-1] {
		t.Fatalf("cycle chain = %v; want a closed chain", cycle.Chain)
	}
	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("Resolve error type = %T; want *PathError (%v)", err, err)
	}
}

// TestResolveEnvironmentFallback verifies environment fallback and defaults.
func TestResolveEnvironmentFallback(t *testing.T) {
	t.Setenv("FASTCONF_EXPR_TEST_HOST", "envhost")
	t.Setenv("FASTCONF_EXPR_TEST_EMPTY", "")

	root := map[string]any{
		"fromEnv":       "${fastconf.expr.test.host}",
		"emptyEnv":      "${fastconf.expr.test.empty}",
		"withDefault":   "${fastconf.expr.test.missing:fallback}",
		"fromCondition": `#{ ${fastconf.expr.test.host} == "envhost" ? "match" : "no" }`,
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if root["fromEnv"] != "envhost" {
		t.Fatalf("fromEnv = %#v; want %q", root["fromEnv"], "envhost")
	}
	if root["emptyEnv"] != "" {
		t.Fatalf("emptyEnv = %#v; want an empty string", root["emptyEnv"])
	}
	if root["withDefault"] != "fallback" {
		t.Fatalf("withDefault = %#v; want %q", root["withDefault"], "fallback")
	}
	if root["fromCondition"] != "match" {
		t.Fatalf("fromCondition = %#v; want %q", root["fromCondition"], "match")
	}
}

// TestResolveConfigurationPrecedence verifies that document values win over
// environment variables.
func TestResolveConfigurationPrecedence(t *testing.T) {
	t.Setenv("FASTCONF_EXPR_TEST_NAME", "from-environment")

	root := map[string]any{
		"fastconf": map[string]any{
			"expr": map[string]any{
				"test": map[string]any{"name": "from-config"},
			},
		},
		"value": "${fastconf.expr.test.name}",
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if root["value"] != "from-config" {
		t.Fatalf("value = %#v; want %q", root["value"], "from-config")
	}
}

// TestResolveEmptyString verifies that a present empty value is not replaced by
// a default.
func TestResolveEmptyString(t *testing.T) {
	t.Parallel()

	root := map[string]any{"empty": "", "value": "${empty:fallback}"}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if root["value"] != "" {
		t.Fatalf("value = %#v; want an empty string", root["value"])
	}
}

// TestResolveLeavesPlainValues verifies the no-marker fast path and scalar
// preservation.
func TestResolveLeavesPlainValues(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"int":    42,
		"float":  1.5,
		"bool":   true,
		"nil":    nil,
		"plain":  "plain text",
		"quoted": `"quoted"`,
		"star":   "glob*",
		"dollar": "cost$",
		"hash":   "tag#",
		"nested": map[string]any{"value": "text"},
		"list":   []any{"a", 1, true},
	}
	before := make(map[string]any, len(root))
	for key, value := range root {
		before[key] = value
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if !reflect.DeepEqual(root, before) {
		t.Fatalf("Resolve changed a document without placeholders:\n got %#v\nwant %#v", root, before)
	}
}

// TestResolveNilDocument verifies that a nil document is a no-op.
func TestResolveNilDocument(t *testing.T) {
	t.Parallel()

	if err := Resolve(context.Background(), nil); err != nil {
		t.Fatalf("Resolve(nil) error = %v", err)
	}
}

// TestResolveCanceledContext verifies context cancellation.
func TestResolveCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	root := map[string]any{"name": "svc", "value": "${name}"}
	err := Resolve(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve error = %v; want context.Canceled", err)
	}
}

// TestResolveIdempotent verifies that resolving twice yields the same document.
func TestResolveIdempotent(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"name": "svc",
		"url":  "http://${name}",
		"list": []any{"*${missing:[a, b]}", "${name}"},
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("first Resolve error = %v", err)
	}
	first := make(map[string]any, len(root))
	first["name"], first["url"], first["list"] = root["name"], root["url"], root["list"]
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("second Resolve error = %v", err)
	}
	if !reflect.DeepEqual(root["url"], first["url"]) {
		t.Fatalf("url after second Resolve = %#v; want %#v", root["url"], first["url"])
	}
	if !reflect.DeepEqual(root["list"], first["list"]) {
		t.Fatalf("list after second Resolve = %#v; want %#v", root["list"], first["list"])
	}
}

// TestResolveListOfLists verifies nested list resolution.
func TestResolveListOfLists(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"name": "svc",
		"matrix": []any{
			[]any{"${name}", []any{"${name}-deep"}},
		},
	}
	if err := Resolve(context.Background(), root); err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	matrix, ok := root["matrix"].([]any)
	if !ok || len(matrix) != 1 {
		t.Fatalf("matrix = %#v; want one row", root["matrix"])
	}
	row, ok := matrix[0].([]any)
	if !ok || len(row) != 2 {
		t.Fatalf("matrix row = %#v; want two cells", matrix[0])
	}
	if row[0] != "svc" {
		t.Fatalf("matrix[0][0] = %#v; want %q", row[0], "svc")
	}
	inner, ok := row[1].([]any)
	if !ok || len(inner) != 1 || inner[0] != "svc-deep" {
		t.Fatalf("matrix[0][1] = %#v; want [svc-deep]", row[1])
	}
}

// TestEnvironmentName verifies path to environment variable conversion.
func TestEnvironmentName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{path: "database.host", want: "DATABASE_HOST"},
		{path: "tenant1.name", want: "TENANT1_NAME"},
		{path: "already_snake", want: "ALREADY_SNAKE"},
		{path: "with-dash.value", want: "WITH_DASH_VALUE"},
		{path: "spaced path.value", want: "SPACED_PATH_VALUE"},
		{path: "..", want: ""},
	}
	for _, tt := range tests {
		if got := environmentName(tt.path); got != tt.want {
			t.Errorf("environmentName(%q) = %q; want %q", tt.path, got, tt.want)
		}
	}
}

// checkList compares a resolved list with an expected list.
func checkList(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v; want %#v", name, got, want)
	}
}
