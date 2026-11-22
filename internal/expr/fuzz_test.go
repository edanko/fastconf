package expr

import (
	"context"
	"testing"
)

// FuzzCompileAndResolve verifies that arbitrary template text never panics the
// compiler, the evaluator or the document resolver.
func FuzzCompileAndResolve(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		"${name}",
		"${missing:fallback}",
		"${missing:[a, 2, true]}",
		"*${list}",
		"pre-${name}-post",
		"#{ true }",
		`#{ ${port} > 1024 ? "high" : "low" }`,
		`#{ !${enabled} and (${name} == "svc" or ${port} >= 1) }`,
		`#{ "x-#{ false ? 1 : 2 }" }`,
		`"quoted"`,
		"*",
		"$",
		"#",
		"${",
		"#{",
		"${a..b}",
		"#{ ( }",
		`${missing:"a}b"}`,
		"*${list}tail",
		"x*${list}",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, source string) {
		template, err := Compile(source)
		if err == nil && template != nil {
			// Evaluation is allowed to fail on arbitrary input; it must not
			// panic.
			_, _ = template.Eval(fuzzScope{})
		}

		root := map[string]any{
			"a": source,
			"b": []any{source, "${a}", "*${c}"},
			"c": []any{"one", "two"},
			"d": map[string]any{"nested": source, "flag": true},
		}
		_ = Resolve(context.Background(), root)
	})
}

// fuzzScope is the fixed scope used by the fuzz target.
type fuzzScope struct{}

// Lookup implements Scope.
func (fuzzScope) Lookup(path string) (any, bool) {
	switch path {
	case "a", "name":
		return "value", true
	case "b", "port":
		return 7, true
	case "c", "enabled":
		return true, true
	case "d", "list":
		return []any{"x", "y"}, true
	case "e":
		return map[string]any{"f": "g"}, true
	default:
		return nil, false
	}
}
