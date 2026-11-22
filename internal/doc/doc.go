// Package doc normalizes configuration documents.
//
// A configuration document is either already wrapped in a "config" object
// (the canonical form used by the hierarchy) or a bare object whose keys are
// configuration values. Both forms are accepted from every source.
package doc

import "github.com/edanko/fastconf/internal/sections"

// Normalize returns a document that is guaranteed to have a configuration root
// object under the "config" key.
func Normalize(document map[string]any) map[string]any {
	if document == nil {
		return map[string]any{sections.Root: map[string]any{}}
	}
	if _, ok := document[sections.Root].(map[string]any); ok {
		return document
	}
	return map[string]any{sections.Root: document}
}

// Root returns the configuration root object of a normalized document.
func Root(document map[string]any) (map[string]any, bool) {
	root, ok := document[sections.Root].(map[string]any)
	return root, ok
}

// IsEmpty reports whether the document holds no configuration values.
func IsEmpty(document map[string]any) bool {
	root, ok := Root(document)
	return !ok || len(root) == 0
}
