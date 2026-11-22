package expr

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/edanko/fastconf/internal/maps"
)

// mapScope is a Scope backed by a nested map with case-insensitive keys.
type mapScope map[string]any

// Lookup implements Scope.
func (s mapScope) Lookup(path string) (any, bool) {
	segments := strings.Split(path, ".")
	for i := range segments {
		segments[i] = strings.TrimSpace(segments[i])
	}
	return maps.GetFold(map[string]any(s), segments)
}

// testScope contains values shared by the table-driven evaluation tests.
func testScope() mapScope {
	return mapScope{
		"name":      "svc",
		"port":      8080,
		"ratio":     0.5,
		"enabled":   true,
		"empty":     "",
		"zero":      0,
		"list":      []any{"a", "b"},
		"nested":    map[string]any{"child": "deep"},
		"chain":     "${name}-${port}",
		"loopA":     "${loopB}",
		"loopB":     "${loopA}",
		"greeting":  "hello",
		"age":       41,
		"threshold": 100,
	}
}

// TestEvaluate covers every template syntax form and evaluation rule.
func TestEvaluate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		want      any
		wantError string
	}{
		// Literal strings and the no-marker fast path.
		{name: "literal", source: "plain text", want: "plain text"},
		{name: "empty literal", source: "", want: ""},
		{name: "double quotes preserved", source: `"quoted"`, want: `"quoted"`},
		{name: "single quotes preserved", source: `'quoted'`, want: `'quoted'`},
		{name: "trailing dollar", source: "cost$", want: "cost$"},
		{name: "trailing hash", source: "tag#", want: "tag#"},
		{name: "trailing star", source: "glob*", want: "glob*"},
		{name: "marker-like text", source: "a$ b# c* d{e} ", want: "a$ b# c* d{e} "},
		{name: "hash without brace", source: "a#b", want: "a#b"},
		{name: "dollar without brace", source: "a$b", want: "a$b"},
		{name: "stray brace", source: "a}b", want: "a}b"},

		// ${path} lookups and typed results.
		{name: "lookup string", source: "${name}", want: "svc"},
		{name: "lookup int", source: "${port}", want: 8080},
		{name: "lookup float", source: "${ratio}", want: 0.5},
		{name: "lookup bool", source: "${enabled}", want: true},
		{name: "lookup empty string", source: "${empty}", want: ""},
		{name: "lookup list", source: "${list}", want: []any{"a", "b"}},
		{name: "lookup object", source: "${nested}", want: map[string]any{"child": "deep"}},
		{name: "lookup case insensitive", source: "${NAME}", want: "svc"},
		{name: "lookup nested path", source: "${nested.child}", want: "deep"},
		{name: "lookup spaced path", source: "${ name }", want: "svc"},
		{name: "recursive resolution", source: "${chain}", want: "svc-8080"},

		// Typed defaults.
		{name: "default string", source: "${missing:fallback}", want: "fallback"},
		{name: "default true", source: "${missing:true}", want: true},
		{name: "default false", source: "${missing:false}", want: false},
		{name: "default number", source: "${missing:42}", want: float64(42)},
		{name: "default negative number", source: "${missing:-1.5}", want: -1.5},
		{name: "default positive sign", source: "${missing:+7}", want: float64(7)},
		{name: "default quoted", source: `${missing:"quoted default"}`, want: "quoted default"},
		{name: "default single quoted", source: `${missing:'single'}`, want: "single"},
		{name: "default bare duration", source: "${missing:5s}", want: "5s"},
		{name: "default bare url", source: "${missing:http://example.test/x}", want: "http://example.test/x"},
		{name: "default list", source: `${missing:[a, 2, true, "b,c"]}`, want: []any{"a", float64(2), true, "b,c"}},
		{name: "default empty list", source: "${missing:[]}", want: []any{}},
		{name: "default empty", source: "${missing:}", want: ""},
		{name: "default spaced", source: "${missing: 42 }", want: float64(42)},
		{name: "present empty beats default", source: "${empty:fallback}", want: ""},
		{name: "default with brace quoted", source: `${missing:"a}b"}`, want: "a}b"},
		{name: "default escaped quote", source: `${missing:"a\"b"}`, want: `a"b`},

		// Concatenation.
		{name: "concatenate", source: "db-${name}-${port}", want: "db-svc-8080"},
		{name: "concatenate bool", source: "${name}:${enabled}", want: "svc:true"},
		{name: "concatenate float", source: "${name}-${ratio}", want: "svc-0.5"},
		{name: "adjacent lookups", source: "${name}${port}", want: "svc8080"},
		{name: "quote wrapped lookup", source: `"${name}"`, want: `"svc"`},
		{name: "default quoted with marker text", source: `${missing:"literal ${name}"}`, want: `literal ${name}`},

		// Array unpack templates.
		{name: "unpack", source: "*${list}", want: []any{"a", "b"}},
		{name: "unpack default list", source: "*${missing:[a, b]}", want: []any{"a", "b"}},
		{name: "unpack suffix", source: "*${list}suffix", wantError: "must be the entire value"},
		{name: "unpack prefix", source: "pre*${list}", wantError: "must be the entire value or the entire list element"},
		{name: "unpack missing", source: "*${missing}", wantError: "is not defined"},

		// Compile-time errors.
		{name: "empty path", source: "${}", wantError: "empty placeholder path"},
		{name: "unclosed placeholder", source: "${name", wantError: "unclosed placeholder"},
		{name: "empty path segment", source: "${a..b}", wantError: "empty segment"},
		{name: "unterminated default quote", source: `${missing:"abc}`, wantError: "unterminated quoted string"},
		{name: "unclosed array default", source: "${missing:[a, b}", wantError: "unclosed array default"},
		{name: "empty array element", source: "${missing:[a,,b]}", wantError: "empty element in array default"},
		{name: "nested array default", source: "${missing:[[a]]}", wantError: "nested array defaults"},
		{name: "text after quoted default", source: `${missing:"a"b}`, wantError: "unexpected text after quoted default"},

		// #{...} conditionals.
		{name: "conditional true", source: "#{ true }", want: true},
		{name: "conditional false", source: "#{ false }", want: false},
		{name: "keywords case insensitive", source: "#{ TRUE AND FALSE }", want: false},
		{name: "not", source: "#{ !${enabled} }", want: false},
		{name: "double not", source: "#{ !!${enabled} }", want: true},
		{name: "and or precedence", source: "#{ false or true and true }", want: true},
		{name: "parentheses", source: "#{ (false or true) and false }", want: false},
		{name: "comparison numeric", source: "#{ ${port} >= 8000 }", want: true},
		{name: "comparison cross type", source: "#{ ${port} == 8080 }", want: true},
		{name: "comparison less", source: "#{ ${age} < ${threshold} }", want: true},
		{name: "comparison not equal", source: `#{ ${name} != "other" }`, want: true},
		{name: "comparison string equality", source: `#{ ${name} == "svc" }`, want: true},
		{name: "comparison string order", source: `#{ "alpha" < "beta" }`, want: true},
		{name: "ternary", source: `#{ ${port} > 1024 ? "high" : "low" }`, want: "high"},
		{name: "ternary typed numbers", source: "#{ false ? 1 : 2 }", want: float64(2)},
		{name: "nested ternary", source: `#{ ${port} == 1 ? "one" : ${port} == 8080 ? "many" : "other" }`, want: "many"},
		{name: "nested quoted conditional", source: `#{ true ? "x-#{ false ? 1 : 2 }" : "no" }`, want: "x-2"},
		{name: "quoted branch template", source: `#{ true ? "svc-${name}" : "none" }`, want: "svc-svc"},
		{name: "quoted branch unquoted", source: `#{ false ? 'yes' : "no" }`, want: "no"},
		{name: "quoted branch typed lookup", source: `#{ true ? "${port}" : 0 }`, want: 8080},
		{name: "variable lookup", source: `#{ enabled and name == "svc" }`, want: true},
		{name: "lookup default in expression", source: `#{ ${missing:5} > 3 ? "ok" : "no" }`, want: "ok"},
		{name: "empty string falsy", source: `#{ empty ? "yes" : "no" }`, want: "no"},
		{name: "zero falsy", source: `#{ zero ? "yes" : "no" }`, want: "no"},
		{name: "string truthy", source: `#{ name ? "yes" : "no" }`, want: "yes"},
		{name: "unary minus", source: `#{ -${age} < 0 ? "neg" : "pos" }`, want: "neg"},
		{name: "conditional inside text", source: `v-#{ 1 > 0 ? "on" : "off" }-end`, want: "v-on-end"},
		{name: "escaped quote", source: `#{ "a\"b" == 'a"b' }`, want: true},
		{name: "escaped backslash", source: `#{ "a\\b" == "a\\b" }`, want: true},
		{name: "empty literal equality", source: `#{ "" == "" }`, want: true},
		{name: "braces in quoted branch", source: `#{ "a}b" == "a}b" }`, want: true},
		{name: "single lookup expression typed", source: "#{ ${port} }", want: 8080},
		{name: "quoted template comparison", source: `#{ "${name}" == "svc" }`, want: true},

		// Evaluation errors.
		{name: "missing variable", source: "#{ missing }", wantError: "is not defined"},
		{name: "missing lookup", source: "${missing}", wantError: "is not defined"},
		{name: "cycle", source: "${loopA}", wantError: "placeholder cycle"},
		{name: "order mismatch", source: `#{ 1 < "two" }`, wantError: "cannot order"},
		{name: "negate non number", source: "#{ -\"x\" }", wantError: "cannot negate"},
		{name: "interpolate list", source: "${list}-x", wantError: "cannot interpolate"},

		// Expression syntax errors.
		{name: "empty conditional", source: "#{}", wantError: "empty conditional expression"},
		{name: "unclosed conditional", source: "#{ true", wantError: "unclosed conditional expression"},
		{name: "comparison chain", source: "#{ 1 < 2 < 3 }", wantError: "unexpected '<'"},
		{name: "single equals", source: `#{ name = "x" }`, wantError: "expected '=='"},
		{name: "unexpected character", source: "#{ @ }", wantError: "unexpected character"},
		{name: "unexpected dollar", source: "#{ $ }", wantError: "unexpected '$'"},
		{name: "missing paren", source: "#{ (true }", wantError: "expected ')'"},
		{name: "missing ternary colon", source: "#{ true ? 1 }", wantError: "needs ':'"},
		{name: "missing operand", source: "#{ 1 and }", wantError: "expected a value"},
		{name: "unpack in branch", source: `#{ true ? "*${list}" : "x" }`, wantError: "not allowed inside a conditional"},
		{name: "quoted branch syntax error", source: `#{ true ? "${name" : "x" }`, wantError: "invalid quoted template"},
		{name: "trailing brace literal", source: "#{ true } }", want: "true }"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			template, err := Compile(tt.source)
			if err != nil {
				if tt.wantError == "" {
					t.Fatalf("Compile(%q) error = %v", tt.source, err)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Compile(%q) error = %q; want it to contain %q", tt.source, err, tt.wantError)
				}
				return
			}
			got, err := template.Eval(testScope())
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("Eval(%q) = %#v; want an error containing %q", tt.source, got, tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Eval(%q) error = %q; want it to contain %q", tt.source, err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Eval(%q) error = %v", tt.source, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Eval(%q) = %#v (%T); want %#v (%T)", tt.source, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestCompileErrorOffsets verifies that syntax errors point at the offending
// byte.
func TestCompileErrorOffsets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		offset int
	}{
		{source: "${name", offset: 0},
		{source: "x${}", offset: 3},
		{source: "x#{}", offset: 3},
		{source: "x${a..b}", offset: 3},
		{source: "x#{ 1 ~ 2 }", offset: 6},
		{source: "x*${list}", offset: 1},
		{source: "*${list}tail", offset: 8},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(tt.source)
			if err == nil {
				t.Fatalf("Compile(%q) succeeded; want a compile error", tt.source)
			}
			var compileErr *CompileError
			if !errors.As(err, &compileErr) {
				t.Fatalf("Compile(%q) error type = %T; want *CompileError", tt.source, err)
			}
			if compileErr.Offset != tt.offset {
				t.Fatalf("Compile(%q) offset = %d; want %d (%v)", tt.source, compileErr.Offset, tt.offset, err)
			}
		})
	}
}

// TestUnpackFlag verifies unpack detection.
func TestUnpackFlag(t *testing.T) {
	t.Parallel()

	unpack, err := Compile("*${list}")
	if err != nil {
		t.Fatalf("Compile(*${list}) error = %v", err)
	}
	if !unpack.Unpack() {
		t.Fatal("Compile(*${list}).Unpack() = false; want true")
	}
	if unpack.Source() != "*${list}" {
		t.Fatalf("Source() = %q; want %q", unpack.Source(), "*${list}")
	}

	plain, err := Compile("${list}")
	if err != nil {
		t.Fatalf("Compile(${list}) error = %v", err)
	}
	if plain.Unpack() {
		t.Fatal("Compile(${list}).Unpack() = true; want false")
	}
}

// TestCompileCache verifies that marker templates are cached and that the cache
// is safe under concurrency.
func TestCompileCache(t *testing.T) {
	t.Parallel()

	source := `#{ ${environment} == "production" ? "prod-${database.host}" : "local" }`
	first, err := Compile(source)
	if err != nil {
		t.Fatalf("Compile(%q) error = %v", source, err)
	}
	second, err := Compile(source)
	if err != nil {
		t.Fatalf("Compile(%q) error = %v", source, err)
	}
	if first != second {
		t.Fatal("Compile did not return the cached template")
	}

	var waitGroup sync.WaitGroup
	for worker := range 8 {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			scope := mapScope{"environment": "production", "database": map[string]any{"host": "db"}}
			for range 200 {
				template, err := Compile(source)
				if err != nil {
					t.Errorf("worker %d: Compile error = %v", worker, err)
					return
				}
				value, err := template.Eval(scope)
				if err != nil {
					t.Errorf("worker %d: Eval error = %v", worker, err)
					return
				}
				if value != "prod-db" {
					t.Errorf("worker %d: Eval = %#v; want %q", worker, value, "prod-db")
					return
				}
			}
		}(worker)
	}
	waitGroup.Wait()
}

// TestEvalNilGuards verifies nil guards do not panic.
func TestEvalNilGuards(t *testing.T) {
	t.Parallel()

	var template *Template
	if _, err := template.Eval(testScope()); err == nil {
		t.Fatal("nil Template.Eval returned nil error")
	}
	compiled, err := Compile("${name}")
	if err != nil {
		t.Fatalf("Compile error = %v", err)
	}
	if _, err := compiled.Eval(nil); err == nil {
		t.Fatal("Eval(nil) returned nil error")
	}
}

// TestDocumentScopeEmpty verifies that an empty scope reports missing paths.
func TestDocumentScopeEmpty(t *testing.T) {
	t.Parallel()

	template, err := Compile("${missing}")
	if err != nil {
		t.Fatalf("Compile error = %v", err)
	}
	if _, err := template.Eval(mapScope{}); err == nil {
		t.Fatal("Eval with an empty scope returned nil error")
	}
}

// TestResolveNilContextGuard exercises Resolve with a nil context argument.
func TestResolveNilContextGuard(t *testing.T) {
	t.Parallel()

	root := map[string]any{"name": "svc", "copy": "${name}"}
	if err := Resolve(nil, root); err != nil { //nolint:staticcheck // nil context is part of the contract under test
		t.Fatalf("Resolve(nil, values) error = %v", err)
	}
	if root["copy"] != "svc" {
		t.Fatalf("copy = %#v; want %q", root["copy"], "svc")
	}
}
