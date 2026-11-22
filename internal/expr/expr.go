// Package expr compiles and resolves configuration placeholder templates.
//
// A configuration document is a tree of map[string]any objects, []any lists and
// scalar values. String values in that tree may contain placeholders:
//
//	${path}             typed lookup of a configuration value
//	${path:default}     the same lookup with a typed fallback value
//	#{ expression }     conditional expression
//	*${path}            list unpack
//
// Templates are compiled once, cached, and evaluated against a Scope.
// [Resolve] walks a configuration root object, resolves every template in place
// and implements the document rules: case-insensitive paths, environment
// variable fallback and list splicing.
//
// # Fragments
//
// A template is a sequence of literal text and placeholder fragments. A string
// that consists of a single placeholder fragment evaluates to the typed value
// of that fragment; several fragments are concatenated into a string. Strings
// without a "${" or "#{" marker are returned unchanged without parsing.
//
// # Lookups
//
// ${path} traverses objects only, matching keys case-insensitively; the path
// separator is ".". When the path is missing and a default was written after a
// colon, the default is returned. Defaults are typed: true and false are
// booleans, numbers are float64, quoted strings are unquoted, bare strings such
// as 5s are strings and [a, 2, true] is a typed list. A referenced string value
// is resolved recursively and placeholder cycles are reported as *CycleError.
//
// # Conditionals
//
// #{ expression } evaluates a small expression language with the operators !,
// and, or, comparisons (==, !=, >, >=, <, <=), parentheses and the ternary
// operator condition ? then : else. Operands may be numbers, quoted strings,
// true, false, variable paths, ${path} lookups and nested conditionals. A
// quoted string inside an expression is a template: "svc-${name}" is resolved
// and unquoted, so quoted branches return plain text rather than quotes.
//
// # Environment fallback
//
// [Resolve] looks a path up in the configuration document first and then falls
// back to an environment variable named after the path: segments are joined by
// an underscore and upper-cased, so "database.host" reads DATABASE_HOST, and
// "tenant1/name" reads TENANT1_NAME. The fallback also applies to variable
// lookups inside conditional expressions.
package expr

import (
	"errors"
	"strings"
	"sync"
)

// Scope resolves placeholder paths during template evaluation. A Scope is
// responsible for its own path syntax, case sensitivity and fallback rules.
type Scope interface {
	Lookup(path string) (any, bool)
}

// Template is a compiled placeholder template. Templates are immutable, so a
// single Template may be evaluated concurrently by any number of goroutines.
type Template struct {
	source    string
	fragments []fragment
	unpack    bool
}

// Source returns the template source text.
func (t *Template) Source() string {
	return t.source
}

// Unpack reports whether the template is an array unpack template of the form
// *${path}. The caller decides whether the evaluated list is spliced into a
// parent list or assigned as a whole value.
func (t *Template) Unpack() bool {
	return t.unpack
}

const (
	// maxCachedTemplates bounds the compiled template cache. The cache resets
	// as a whole when the bound is reached, which keeps memory predictable for
	// long running processes and fuzzing.
	maxCachedTemplates = 4096
	// maxCompileDepth bounds recursion through quoted templates inside
	// conditional expressions.
	maxCompileDepth = 64
	// maxExpressionDepth bounds nesting of parentheses, ternaries and unary
	// operators while parsing an expression.
	maxExpressionDepth = 256
	// maxLookupDepth bounds chains of placeholders that reference each other.
	// Cycles are detected separately; the depth limit protects against a Scope
	// that fabricates an unbounded chain of new paths.
	maxLookupDepth = 128
)

var templateCache = struct {
	mu      sync.Mutex
	entries map[string]*Template
}{
	entries: make(map[string]*Template, maxCachedTemplates),
}

// Compile parses source into a Template. Templates without a placeholder
// marker are returned as a literal template without parsing. Successful
// compilations are cached by source text, so compiling the same placeholder
// repeatedly is cheap. Errors are returned as *CompileError with a byte offset.
func Compile(source string) (*Template, error) {
	if !hasMarkers(source) {
		return &Template{source: source}, nil
	}

	templateCache.mu.Lock()
	cached, ok := templateCache.entries[source]
	templateCache.mu.Unlock()
	if ok {
		return cached, nil
	}

	compiled, err := compileTemplate(source, 0)
	if err != nil {
		return nil, err
	}

	templateCache.mu.Lock()
	if len(templateCache.entries) >= maxCachedTemplates {
		templateCache.entries = make(map[string]*Template, maxCachedTemplates)
	}
	templateCache.entries[source] = compiled
	templateCache.mu.Unlock()

	return compiled, nil
}

// Eval evaluates the template against scope and returns its typed value. A
// template with a single placeholder fragment returns that fragment's value
// unchanged; several fragments are concatenated into a string. Values
// referenced by a placeholder are resolved recursively, and cycles are reported
// as *CycleError.
func (t *Template) Eval(scope Scope) (any, error) {
	if t == nil {
		return nil, errors.New("expr: cannot evaluate a nil template")
	}
	if scope == nil {
		return nil, errors.New("expr: cannot evaluate a template with a nil scope")
	}
	ctx := &evalContext{scope: scope}
	return t.eval(ctx)
}

// hasMarkers reports whether source contains a placeholder start marker.
func hasMarkers(source string) bool {
	return strings.Contains(source, "${") || strings.Contains(source, "#{")
}
