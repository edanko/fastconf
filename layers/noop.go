package layers

import "context"

// Noop returns values unchanged. It is useful as a placeholder source.
type Noop struct{}

// NewNoop creates a no-op source.
func NewNoop() Noop { return Noop{} }

// Load implements Layer.
func (Noop) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	return values, nil
}
