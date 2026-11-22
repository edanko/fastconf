package fastconf_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

func overrideDocument() map[string]any {
	return map[string]any{
		"config": map[string]any{
			"domain":   "source.example",
			"database": map[string]any{"host": "source-db", "port": 5432},
			"tenants": map[string]any{
				"acme": map[string]any{
					"domain":   "acme.example",
					"database": map[string]any{"host": "acme-db"},
				},
				"globex": map[string]any{"domain": "globex.example"},
				"scalar": map[string]any{"database": "flat"},
			},
		},
	}
}

func newOverrideLoader(t *testing.T, document map[string]any) *fastconf.Loader {
	t.Helper()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(document)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })
	return loader
}

func rootMap(t *testing.T, loader *fastconf.Loader) map[string]any {
	t.Helper()
	values, err := loader.Map(context.Background())
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	return values
}

func TestOverrideAppliesToRootAndTenants(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())

	if err := loader.SetOverride("domain", "runtime.example"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	root := rootMap(t, loader)
	if root["domain"] != "runtime.example" {
		t.Fatalf("root domain = %#v", root["domain"])
	}
	view, err := loader.View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if domain, _ := view.String("domain"); domain != "runtime.example" {
		t.Fatalf("view domain = %q", domain)
	}
	acme, err := loader.TenantMap(context.Background(), "acme")
	if err != nil {
		t.Fatalf("TenantMap: %v", err)
	}
	if acme["domain"] != "runtime.example" {
		t.Fatalf("tenant domain = %#v", acme["domain"])
	}

	removed, err := loader.DeleteOverride("domain")
	if err != nil || !removed {
		t.Fatalf("DeleteOverride = %v, %v", removed, err)
	}
	if domain := rootMap(t, loader)["domain"]; domain != "source.example" {
		t.Fatalf("root domain after delete = %#v", domain)
	}
	if domain := mustTenant(t, loader, "acme")["domain"]; domain != "acme.example" {
		t.Fatalf("tenant domain after delete = %#v", domain)
	}
}

func TestTenantOverrideIsIsolated(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())

	if err := loader.SetTenantOverride("ACME", "domain", "acme-runtime"); err != nil {
		t.Fatalf("SetTenantOverride: %v", err)
	}
	if domain := mustTenant(t, loader, "acme")["domain"]; domain != "acme-runtime" {
		t.Fatalf("acme domain = %#v", domain)
	}
	if domain := mustTenant(t, loader, "globex")["domain"]; domain != "globex.example" {
		t.Fatalf("globex domain = %#v", domain)
	}
	if domain := rootMap(t, loader)["domain"]; domain != "source.example" {
		t.Fatalf("root domain = %#v", domain)
	}

	removed, err := loader.DeleteTenantOverride("acme", "domain")
	if err != nil || !removed {
		t.Fatalf("DeleteTenantOverride = %v, %v", removed, err)
	}
	if domain := mustTenant(t, loader, "acme")["domain"]; domain != "acme.example" {
		t.Fatalf("acme domain after delete = %#v", domain)
	}
}

func TestOverrideSurvivesReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mutex sync.RWMutex
	document := overrideDocument()
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

	if err := loader.SetOverride("domain", "runtime.example"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	mutex.Lock()
	document = map[string]any{"config": map[string]any{"domain": "reloaded.example", "tenants": map[string]any{"acme": map[string]any{"domain": "acme.example"}}}}
	mutex.Unlock()
	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if domain := rootMap(t, loader)["domain"]; domain != "runtime.example" {
		t.Fatalf("override did not survive reload: %#v", domain)
	}
	if _, err := loader.DeleteOverride("domain"); err != nil {
		t.Fatalf("DeleteOverride: %v", err)
	}
	if domain := rootMap(t, loader)["domain"]; domain != "reloaded.example" {
		t.Fatalf("source value after delete = %#v", domain)
	}
}

func TestOverrideCreatesNestedPathsCaseInsensitively(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())

	if err := loader.SetOverride("Features.NewFlag", true); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if err := loader.SetOverride("features.newflag", false); err != nil {
		t.Fatalf("SetOverride second: %v", err)
	}
	features, ok := rootMap(t, loader)["Features"].(map[string]any)
	if !ok {
		t.Fatalf("Features = %#v", rootMap(t, loader)["Features"])
	}
	if features["NewFlag"] != false {
		t.Fatalf("NewFlag = %#v", features["NewFlag"])
	}
}

func TestOverrideReplacesScalarIntermediate(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())

	if err := loader.SetTenantOverride("scalar", "database.host", "nested-host"); err != nil {
		t.Fatalf("SetTenantOverride: %v", err)
	}
	database, ok := mustTenant(t, loader, "scalar")["database"].(map[string]any)
	if !ok {
		t.Fatalf("database = %#v, want an object", mustTenant(t, loader, "scalar")["database"])
	}
	if database["host"] != "nested-host" {
		t.Fatalf("host = %#v", database["host"])
	}
}

func TestOverrideBeforeFirstLoad(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())
	if err := loader.SetOverride("domain", "early.example"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if domain := rootMap(t, loader)["domain"]; domain != "early.example" {
		t.Fatalf("domain = %#v", domain)
	}
}

func TestOverridePathsAreValidated(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())

	for _, test := range []struct {
		name string
		call func() error
	}{
		{"empty", func() error { return loader.SetOverride("", 1) }},
		{"empty segment", func() error { return loader.SetOverride("a..b", 1) }},
		{"reserved tenants", func() error { return loader.SetOverride("tenants.acme.domain", "x") }},
		{"empty tenant", func() error { return loader.SetTenantOverride("", "domain", "x") }},
		{"delete reserved", func() error {
			_, err := loader.DeleteOverride("tenants.acme.domain")
			return err
		}},
	} {
		if err := test.call(); err == nil {
			t.Fatalf("%s path was accepted", test.name)
		}
	}
}

func TestOverrideForMissingTenantAppliesAfterReload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mutex sync.RWMutex
	document := map[string]any{"config": map[string]any{"domain": "source", "tenants": map[string]any{"acme": map[string]any{"domain": "acme"}}}}
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

	if err := loader.SetTenantOverride("later", "domain", "later-runtime"); err != nil {
		t.Fatalf("SetTenantOverride: %v", err)
	}
	mutex.Lock()
	document = map[string]any{"config": map[string]any{"domain": "source", "tenants": map[string]any{
		"acme":  map[string]any{"domain": "acme"},
		"later": map[string]any{"domain": "later"},
	}}}
	mutex.Unlock()
	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if domain := mustTenant(t, loader, "later")["domain"]; domain != "later-runtime" {
		t.Fatalf("later domain = %#v", domain)
	}
}

func TestOverrideConcurrentReads(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())
	if err := loader.SetOverride("domain", "runtime.example"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	ctx := context.Background()
	var wait sync.WaitGroup
	var failures sync.Map
	for worker := range 8 {
		wait.Go(func() {
			for iteration := range 50 {
				values, err := loader.Map(ctx)
				if err != nil {
					failures.Store("map", err)
					return
				}
				if values["domain"] != "runtime.example" && values["domain"] != "source.example" {
					failures.Store("domain", fmt.Sprintf("%#v", values["domain"]))
					return
				}
				if worker == 0 && iteration%10 == 0 {
					_, _ = loader.DeleteOverride("domain")
					_ = loader.SetOverride("domain", "runtime.example")
				}
			}
		})
	}
	wait.Wait()
	failures.Range(func(key, value any) bool {
		t.Fatalf("concurrent override read failed: %v=%v", key, value)
		return false
	})
}

func TestOverrideAfterClose(t *testing.T) {
	t.Parallel()
	loader := newOverrideLoader(t, overrideDocument())
	_ = loader.Reload(context.Background())
	if err := loader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := loader.SetOverride("domain", "late"); !errors.Is(err, fastconf.ErrClosed) {
		t.Fatalf("SetOverride after close = %v, want ErrClosed", err)
	}
}

func mustTenant(t *testing.T, loader *fastconf.Loader, code string) map[string]any {
	t.Helper()
	values, err := loader.TenantMap(context.Background(), code)
	if err != nil {
		t.Fatalf("TenantMap(%q): %v", code, err)
	}
	return values
}
