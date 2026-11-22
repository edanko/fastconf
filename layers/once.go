package layers

import (
	"context"
	"sync"
)

// Once caches the document produced by its inner layer after the first
// successful load.
type Once struct {
	inner Layer

	mutex    sync.Mutex
	loaded   bool
	document map[string]any
}

// NewOnce wraps a layer with single-load caching.
func NewOnce(inner Layer) *Once {
	return &Once{inner: inner}
}

// Load implements Layer.
func (l *Once) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.loaded {
		return l.document, nil
	}
	document, err := l.inner.Load(ctx, values)
	if err != nil {
		return nil, err
	}
	l.document = document
	l.loaded = true
	return document, nil
}

// Reset drops the cached document.
func (l *Once) Reset() {
	l.mutex.Lock()
	l.document = nil
	l.loaded = false
	l.mutex.Unlock()
}
