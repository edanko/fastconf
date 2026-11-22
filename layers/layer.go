// Package layers contains configuration sources.
//
// A source is a Layer: it receives the document accumulated by previous
// sources, merges its own values into it and returns the result. Sources are
// composed with NewChain and executed in order, so later sources override
// earlier ones.
package layers

import "context"

// Layer is a single configuration source or transformation.
//
// Load must not retain values after it returns. Implementations may assume
// that values is non-nil and already normalized (has a "config" root).
type Layer interface {
	Load(ctx context.Context, values map[string]any) (map[string]any, error)
}

// LayerFunc adapts a function into a Layer.
type LayerFunc func(ctx context.Context, values map[string]any) (map[string]any, error)

// Load implements Layer.
func (f LayerFunc) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	return f(ctx, values)
}
