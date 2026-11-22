package fastconf_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

func newViewLoader(t *testing.T) *fastconf.Loader {
	t.Helper()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{
				"domain":   "example.com",
				"port":     8080,
				"ratio":    0.5,
				"enabled":  true,
				"portText": "9090",
				"database": map[string]any{"host": "db.example", "port": 5432},
				"tenants": map[string]any{
					"acme": map[string]any{
						"domain":   "acme.example",
						"port":     9090,
						"database": map[string]any{"host": "acme-db.example"},
					},
				},
			},
		})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })
	return loader
}

func TestViewGetAndTypedGetters(t *testing.T) {
	t.Parallel()
	loader := newViewLoader(t)
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	if host, ok := view.String("database.HOST"); !ok || host != "db.example" {
		t.Fatalf("String = %q, %v", host, ok)
	}
	if port, ok := view.Int("database.port"); !ok || port != 5432 {
		t.Fatalf("Int = %d, %v", port, ok)
	}
	if port, ok := view.Int("portText"); !ok || port != 9090 {
		t.Fatalf("Int from string = %d, %v", port, ok)
	}
	if ratio, ok := view.Float64("ratio"); !ok || ratio != 0.5 {
		t.Fatalf("Float64 = %v, %v", ratio, ok)
	}
	if enabled, ok := view.Bool("enabled"); !ok || !enabled {
		t.Fatalf("Bool = %v, %v", enabled, ok)
	}
	if !view.Has("database.host") {
		t.Fatal("Has returned false for an existing path")
	}
	if view.Has("database.missing") {
		t.Fatal("Has returned true for a missing path")
	}
	if _, ok := view.Int("domain"); ok {
		t.Fatal("Int unexpectedly converted a non-numeric string")
	}
}

func TestViewContainerCopies(t *testing.T) {
	t.Parallel()
	loader := newViewLoader(t)
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	raw, ok := view.Get("database")
	if !ok {
		t.Fatal("Get returned no value")
	}
	database := raw.(map[string]any)
	database["host"] = "mutated"
	if host, _ := view.String("database.host"); host != "db.example" {
		t.Fatalf("snapshot was mutated through Get: %q", host)
	}

	copied := view.Map()
	copied["domain"] = "mutated"
	if domain, _ := view.String("domain"); domain != "example.com" {
		t.Fatalf("snapshot was mutated through Map: %q", domain)
	}
}

func TestViewTenant(t *testing.T) {
	t.Parallel()
	loader := newViewLoader(t)
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	tenant, ok := view.Tenant("ACME")
	if !ok {
		t.Fatal("Tenant lookup was not case-insensitive")
	}
	if domain, _ := tenant.String("domain"); domain != "acme.example" {
		t.Fatalf("tenant domain = %q", domain)
	}
	if host, _ := tenant.String("database.host"); host != "acme-db.example" {
		t.Fatalf("tenant host = %q", host)
	}
	var decoded tenantConfig
	if err := tenant.Decode(&decoded); err != nil {
		t.Fatalf("tenant.Decode: %v", err)
	}
	if decoded.Domain != "acme.example" || decoded.Database.Host != "acme-db.example" {
		t.Fatalf("decoded tenant = %#v", decoded)
	}
	if _, ok := view.Tenant("missing"); ok {
		t.Fatal("Tenant returned a view for a missing tenant")
	}

	codes := view.TenantCodes()
	for i := range codes {
		codes[i] = "mutated"
	}
	if again := view.TenantCodes(); again[0] != "acme" {
		t.Fatalf("TenantCodes returned shared memory: %v", again)
	}
}

func TestViewPinsSnapshotAcrossReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mutex sync.RWMutex
	document := map[string]any{"config": map[string]any{"domain": "first"}}
	source := layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		mutex.RLock()
		defer mutex.RUnlock()
		return layers.NewInMemory(document).Load(ctx, values)
	})
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(source),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	view, err := loader.View(ctx)
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	mutex.Lock()
	document = map[string]any{"config": map[string]any{"domain": "second"}}
	mutex.Unlock()
	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if domain, _ := view.String("domain"); domain != "first" {
		t.Fatalf("pinned view observed a reload: %q", domain)
	}
	fresh, err := loader.View(ctx)
	if err != nil {
		t.Fatalf("fresh View: %v", err)
	}
	if domain, _ := fresh.String("domain"); domain != "second" {
		t.Fatalf("fresh view domain = %q", domain)
	}
}

func TestViewValidation(t *testing.T) {
	t.Parallel()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithValidator(func(any) error { return errors.New("invalid") }),
		fastconf.WithSources(layers.NewInMemory(map[string]any{"config": map[string]any{"domain": "x"}})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var config applicationConfig
	if err := view.Decode(&config); err == nil {
		t.Fatal("View.Decode skipped validation")
	}
}

func TestViewConcurrentReads(t *testing.T) {
	t.Parallel()
	loader := newViewLoader(t)
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	tenant, ok := view.Tenant("acme")
	if !ok {
		t.Fatal("tenant view is missing")
	}

	var wait sync.WaitGroup
	var failures atomic.Int64
	for range 8 {
		wait.Go(func() {
			for range 100 {
				if domain, _ := tenant.String("domain"); domain != "acme.example" {
					failures.Add(1)
					return
				}
				var decoded tenantConfig
				if err := tenant.Decode(&decoded); err != nil || decoded.Domain != "acme.example" {
					failures.Add(1)
					return
				}
			}
		})
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent view reads failed %d times", failures.Load())
	}
}

func TestViewWriteJSON(t *testing.T) {
	t.Parallel()
	loader := newViewLoader(t)
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var first bytes.Buffer
	if err := view.WriteJSON(&first); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var second bytes.Buffer
	if err := view.WriteJSON(&second); err != nil {
		t.Fatalf("WriteJSON second call: %v", err)
	}
	if first.String() != second.String() {
		t.Fatal("WriteJSON output changed between calls")
	}
	var encoded, copied any
	if err := json.Unmarshal(first.Bytes(), &encoded); err != nil {
		t.Fatalf("unmarshal encoded: %v", err)
	}
	raw, err := json.Marshal(view.Map())
	if err != nil {
		t.Fatalf("marshal Map: %v", err)
	}
	if err := json.Unmarshal(raw, &copied); err != nil {
		t.Fatalf("unmarshal Map: %v", err)
	}
	if !reflect.DeepEqual(encoded, copied) {
		t.Fatalf("WriteJSON = %#v, Map = %#v", encoded, copied)
	}
}
