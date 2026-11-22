package layers

import "context"

// Chain executes its layers in order, feeding the document produced by each
// layer into the next one.
type Chain struct {
	layers []Layer
}

// NewChain creates a source chain.
func NewChain(layers ...Layer) *Chain {
	return &Chain{layers: layers}
}

// Load implements Layer.
func (c *Chain) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	var err error
	for _, layer := range c.layers {
		if layer == nil {
			continue
		}
		values, err = layer.Load(ctx, values)
		if err != nil {
			return nil, err
		}
		if values == nil {
			values = map[string]any{}
		}
	}
	return values, nil
}

// Layers returns the layers in execution order.
func (c *Chain) Layers() []Layer {
	return c.layers
}
