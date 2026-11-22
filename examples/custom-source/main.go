// Command customsourcedemo registers a custom configuration layer, for example
// a secrets manager, between file sources and environment overrides.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

// secretsLayer merges values fetched from a secrets manager.
type secretsLayer struct {
	values map[string]any
}

func (l secretsLayer) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	return layers.NewInMemory(l.values).Load(ctx, values)
}

func main() {
	loader, err := fastconf.New(
		fastconf.WithConfigPath("config.yaml"),
		fastconf.WithLayer(secretsLayer{values: map[string]any{
			"database": map[string]any{"password": "from-secrets-manager"},
		}}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = loader.Close() }()

	password, ok, err := loader.Get(context.Background(), "database.password")
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Printf("password loaded: %v (%v)\n", password, ok)
}
