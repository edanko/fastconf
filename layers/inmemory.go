package layers

import (
	"context"

	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
)

// InMemory is a configuration source backed by an in-memory document.
// It is mostly useful in tests and as a base for composed configurations.
type InMemory struct {
	document map[string]any
}

// NewInMemory creates an in-memory source. The document is copied when the
// source is created, so later mutations do not affect loaded configuration.
func NewInMemory(document map[string]any) *InMemory {
	return &InMemory{document: maps.DeepCopyMap(document)}
}

// Load implements Layer.
func (l *InMemory) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	document := doc.Normalize(values)
	maps.Merge(document, doc.Normalize(l.document))
	return document, nil
}
