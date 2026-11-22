package fastconf_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

type databaseConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type tenantConfig struct {
	TenantCode string         `mapstructure:"tenantCode"`
	Domain     string         `mapstructure:"domain"`
	Feature    string         `mapstructure:"feature"`
	Database   databaseConfig `mapstructure:"database"`
}

type applicationConfig struct {
	Domain     string                  `mapstructure:"domain"`
	Shared     string                  `mapstructure:"shared"`
	AppFeature string                  `mapstructure:"appFeature"`
	Database   databaseConfig          `mapstructure:"database"`
	Tenants    map[string]tenantConfig `mapstructure:"tenants"`
}

func newLoader(t *testing.T, options ...fastconf.Option) *fastconf.Loader {
	t.Helper()
	options = append(options, fastconf.WithoutEnvironmentVariables())
	loader, err := fastconf.New(options...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loader
}

func TestDecodeRootAndTenants(t *testing.T) {
	ctx := context.Background()
	loader := newLoader(t, fastconf.WithSources(layers.NewInMemory(map[string]any{
		"config": map[string]any{
			"domain": "root.example",
			"tenants": map[string]any{
				"acme": map[string]any{
					"domain":  "acme.example",
					"feature": "enabled",
				},
				"beta": map[string]any{"feature": "disabled"},
			},
		},
	})))

	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "root.example" {
		t.Fatalf("domain = %q", config.Domain)
	}
	if len(config.Tenants) != 3 {
		t.Fatalf("root tenants = %d, want 3", len(config.Tenants))
	}
	if config.Tenants["acme"].Domain != "acme.example" {
		t.Fatalf("root tenant = %#v", config.Tenants["acme"])
	}

	var tenant tenantConfig
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.TenantCode != "acme" || tenant.Domain != "acme.example" || tenant.Feature != "enabled" {
		t.Fatalf("unexpected tenant: %#v", tenant)
	}

	codes, err := loader.TenantCodes(ctx)
	if err != nil {
		t.Fatalf("TenantCodes: %v", err)
	}
	want := []string{"acme", "beta", "default"}
	if len(codes) != len(want) {
		t.Fatalf("tenant codes = %v, want %v", codes, want)
	}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("tenant codes = %v, want %v", codes, want)
		}
	}

	var beta tenantConfig
	if err := loader.DecodeTenant(ctx, "BETA", &beta); err != nil {
		t.Fatalf("case-insensitive DecodeTenant: %v", err)
	}
	if beta.TenantCode != "beta" {
		t.Fatalf("tenant code = %q", beta.TenantCode)
	}

	if _, err := loader.TenantCodes(ctx); err != nil {
		t.Fatalf("TenantCodes: %v", err)
	}
	if _, err := loader.TenantMap(ctx, "missing"); !errors.Is(err, fastconf.ErrTenantNotFound) {
		t.Fatalf("TenantMap error = %v", err)
	}

	configurationMap, err := loader.Map(ctx)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if _, ok := configurationMap["config"]; ok {
		t.Fatal("Map must return the configuration root without the document wrapper")
	}
	configurationMap["domain"] = "mutated"
	var again applicationConfig
	if err := loader.Decode(ctx, &again); err != nil {
		t.Fatalf("Decode after mutation: %v", err)
	}
	if again.Domain != "root.example" {
		t.Fatal("Map returned shared memory")
	}

	value, ok, err := loader.Get(ctx, "TENANTS.acme.FEATURE")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v, %v", value, ok, err)
	}
	if value != "enabled" {
		t.Fatalf("Get value = %v", value)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CONFIG_DATABASE__HOST", "env-db")
	t.Setenv("CONFIG_DATABASE__PORT", "7000")

	loader, err := fastconf.New(fastconf.WithSources(layers.NewInMemory(map[string]any{
		"config": map[string]any{"database": map[string]any{"host": "file-db", "port": 5432}},
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Database.Host != "env-db" || config.Database.Port != 7000 {
		t.Fatalf("database = %#v", config.Database)
	}

	var tenant tenantConfig
	if err := loader.DecodeTenant(ctx, "default", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.Database.Host != "env-db" {
		t.Fatalf("tenant database host = %q", tenant.Database.Host)
	}
}

func TestHierarchyAndPlaceholders(t *testing.T) {
	ctx := context.Background()
	loader := newLoader(t,
		fastconf.WithSubsystem("payments"),
		fastconf.WithApplication("billing"),
		fastconf.WithEnvironment("production"),
		fastconf.WithDefaultTenant(""),
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{
				"domain":    "local.example",
				"publicUri": "#{ ${tls} ? \"https\" : \"http\" }://${domain}",
				"tls":       false,
				"subsystems": map[string]any{
					"payments": map[string]any{
						"database": map[string]any{"host": "payments-db"},
						"applications": map[string]any{
							"billing": map[string]any{"appFeature": "billing"},
						},
					},
				},
				"environments": map[string]any{
					"production": map[string]any{
						"domain": "production.example",
						"tls":    true,
						"tenants": map[string]any{
							"acme": map[string]any{"domain": "acme.production.example"},
						},
					},
				},
				"tenants": map[string]any{
					"acme": map[string]any{"database": map[string]any{"port": 6000}},
				},
			},
		})))
	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "production.example" || config.Database.Host != "payments-db" || config.AppFeature != "billing" {
		t.Fatalf("unexpected root: %#v", config)
	}
	value, ok, err := loader.Get(ctx, "publicUri")
	if err != nil || !ok {
		t.Fatalf("Get: %v, %v", ok, err)
	}
	if value != "https://production.example" {
		t.Fatalf("publicUri = %v", value)
	}

	var tenant tenantConfig
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.Domain != "acme.production.example" || tenant.Database.Host != "payments-db" || tenant.Database.Port != 6000 {
		t.Fatalf("unexpected tenant: %#v", tenant)
	}
}

func TestFileHierarchy(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	write := func(relativePath, body string) {
		t.Helper()
		path := filepath.Join(directory, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write("config.yaml", "config:\n  domain: root.example\n  shared: root\n")
	write("subsystems/payments/config.yaml", "config:\n  database:\n    host: subsystem-db\n  tenants:\n    acme:\n      feature: subsystem-feature\n")
	write("subsystems/payments/applications/billing/config.yaml", "config:\n  appFeature: billing\n")
	write("tenants/acme/config.yaml", "config:\n  domain: acme.example\n")
	write("environments/production/config.yaml", "config:\n  domain: production.example\n  tenants:\n    acme:\n      domain: acme.production.example\n")
	write("environments/production/subsystems/payments/config.yaml", "config:\n  database:\n    port: 6432\n")

	loader := newLoader(t,
		fastconf.WithConfigPath(directory),
		fastconf.WithSubsystem("payments"),
		fastconf.WithApplication("billing"),
		fastconf.WithEnvironment("production"),
	)
	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "production.example" || config.Shared != "root" || config.AppFeature != "billing" {
		t.Fatalf("unexpected root: %#v", config)
	}
	if config.Database.Host != "subsystem-db" || config.Database.Port != 6432 {
		t.Fatalf("unexpected database: %#v", config.Database)
	}

	var tenant tenantConfig
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.Domain != "acme.production.example" || tenant.Feature != "subsystem-feature" {
		t.Fatalf("unexpected tenant: %#v", tenant)
	}
	if tenant.Database.Host != "subsystem-db" || tenant.Database.Port != 6432 {
		t.Fatalf("unexpected tenant database: %#v", tenant.Database)
	}
}

func TestReloadAndConcurrentDecode(t *testing.T) {
	ctx := context.Background()
	var mutex sync.RWMutex
	document := map[string]any{"config": map[string]any{"domain": "first"}}
	source := layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		mutex.RLock()
		defer mutex.RUnlock()
		return layers.NewInMemory(document).Load(ctx, values)
	})
	loader := newLoader(t, fastconf.WithSources(source))

	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "first" {
		t.Fatalf("domain = %q", config.Domain)
	}

	mutex.Lock()
	document = map[string]any{"config": map[string]any{"domain": "second"}}
	mutex.Unlock()
	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode after reload: %v", err)
	}
	if config.Domain != "second" {
		t.Fatalf("domain after reload = %q", config.Domain)
	}

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Go(func() {
			for range 50 {
				var config applicationConfig
				if err := loader.Decode(ctx, &config); err != nil {
					t.Errorf("concurrent Decode: %v", err)
					return
				}
				if config.Domain != "second" {
					t.Errorf("concurrent domain = %q", config.Domain)
					return
				}
			}
		})
	}
	waitGroup.Wait()
}

func TestDecodeErrors(t *testing.T) {
	ctx := context.Background()
	loader := newLoader(t, fastconf.WithSources(layers.NewInMemory(map[string]any{
		"config": map[string]any{"domain": 42},
	})))

	if err := loader.Decode(ctx, applicationConfig{}); err == nil {
		t.Fatal("expected an error for a non-pointer destination")
	}
	if err := loader.Decode(ctx, (*applicationConfig)(nil)); err == nil {
		t.Fatal("expected an error for a nil pointer destination")
	}

	strict := newLoader(t,
		fastconf.WithStrictDecoding(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{"domain": "x", "unknown": true},
		})),
	)
	if err := strict.Decode(ctx, &applicationConfig{}); err == nil {
		t.Fatal("expected a strict decoding error")
	}

	validated := newLoader(t,
		fastconf.WithValidator(func(any) error { return errors.New("invalid") }),
		fastconf.WithSources(layers.NewInMemory(map[string]any{"config": map[string]any{"domain": "x"}})),
	)
	if err := validated.Decode(ctx, &applicationConfig{}); err == nil {
		t.Fatal("expected a validation error")
	}
}

type selfValidating struct {
	Domain string `mapstructure:"domain"`
}

func (c *selfValidating) Validate() error {
	if c.Domain == "" {
		return errors.New("domain is required")
	}
	return nil
}

func TestSelfValidation(t *testing.T) {
	ctx := context.Background()
	loader := newLoader(t, fastconf.WithSources(layers.NewInMemory(map[string]any{
		"config": map[string]any{},
	})))
	var config selfValidating
	if err := loader.Decode(ctx, &config); err == nil {
		t.Fatal("expected a self-validation error")
	}
}

func TestScenarioDefaultsAndTenantOverrides(t *testing.T) {
	ctx := context.Background()
	loader := newLoader(t,
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{
				"timeout": "5s",
				"tenants": map[string]any{
					"acme": map[string]any{"timeout": "10s"},
				},
			},
		})))
	type config struct {
		Timeout time.Duration `mapstructure:"timeout"`
	}
	var root config
	if err := loader.Decode(ctx, &root); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if root.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", root.Timeout)
	}
	var tenant config
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.Timeout != 10*time.Second {
		t.Fatalf("tenant timeout = %v", tenant.Timeout)
	}
}
