package fastconf_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

type benchmarkPublicURIs struct {
	AdminAddress  string `mapstructure:"adminAddress"`
	AdminBasepath string `mapstructure:"adminBasepath"`
	AdminURI      string `mapstructure:"adminUri"`
}

type benchmarkTenant struct {
	TenantCode string              `mapstructure:"tenantCode"`
	Domain     string              `mapstructure:"domain"`
	PublicURIs benchmarkPublicURIs `mapstructure:"publicUris"`
}

type benchmarkConfig struct {
	Domain  string                     `mapstructure:"domain"`
	Tenants map[string]benchmarkTenant `mapstructure:"tenants"`
}

func benchmarkDocument(tenantCount int) map[string]any {
	tenants := make(map[string]any, tenantCount)
	for i := 1; i <= tenantCount; i++ {
		code := fmt.Sprintf("tenant%d", i)
		tenants[code] = map[string]any{
			"tenantCode": code,
			"domain":     fmt.Sprintf("tenant%d.example", i),
			"publicUris": map[string]any{
				"adminAddress":  fmt.Sprintf("https://tenant%d.example", i),
				"adminBasepath": fmt.Sprintf("/admin/tenant%d", i),
				"adminUri":      fmt.Sprintf("https://tenant%d.example/admin", i),
			},
		}
	}
	return map[string]any{
		"config": map[string]any{
			"domain":  "local",
			"tenants": tenants,
		},
	}
}

func benchmarkLoader(b *testing.B, tenantCount int) *fastconf.Loader {
	b.Helper()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(benchmarkDocument(tenantCount))),
	)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return loader
}

func BenchmarkDecodeStruct(b *testing.B) {
	ctx := context.Background()
	loader := benchmarkLoader(b, 1000)
	var warm benchmarkConfig
	if err := loader.Decode(ctx, &warm); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		var config benchmarkConfig
		if err := loader.Decode(ctx, &config); err != nil {
			b.Fatalf("Decode: %v", err)
		}
		if len(config.Tenants) != 1001 {
			b.Fatalf("tenants = %d", len(config.Tenants))
		}
	}
}

func BenchmarkDecodeTenantStruct(b *testing.B) {
	ctx := context.Background()
	loader := benchmarkLoader(b, 1000)
	var warm benchmarkTenant
	if err := loader.DecodeTenant(ctx, "tenant1", &warm); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		var tenant benchmarkTenant
		if err := loader.DecodeTenant(ctx, "tenant1", &tenant); err != nil {
			b.Fatalf("DecodeTenant: %v", err)
		}
	}
}

func BenchmarkDecodeTenantMap(b *testing.B) {
	ctx := context.Background()
	loader := benchmarkLoader(b, 1000)
	if _, err := loader.TenantMap(ctx, "tenant1"); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := loader.TenantMap(ctx, "tenant1"); err != nil {
			b.Fatalf("TenantMap: %v", err)
		}
	}
}

func BenchmarkDecodeMap(b *testing.B) {
	ctx := context.Background()
	loader := benchmarkLoader(b, 1000)
	if _, err := loader.Map(ctx); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := loader.Map(ctx); err != nil {
			b.Fatalf("Map: %v", err)
		}
	}
}

func BenchmarkLoadDocument(b *testing.B) {
	ctx := context.Background()
	loader := benchmarkLoader(b, 1000)
	if err := loader.Reload(ctx); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if err := loader.Reload(ctx); err != nil {
			b.Fatalf("Reload: %v", err)
		}
	}
}

func BenchmarkDecodeSmallStruct(b *testing.B) {
	type small struct {
		Host    string `mapstructure:"host"`
		Port    int    `mapstructure:"port"`
		Enabled bool   `mapstructure:"enabled"`
	}
	ctx := context.Background()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{"host": "localhost", "port": 8080, "enabled": true},
		})),
	)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	var warm small
	if err := loader.Decode(ctx, &warm); err != nil {
		b.Fatalf("warm up: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		var config small
		if err := loader.Decode(ctx, &config); err != nil {
			b.Fatalf("Decode: %v", err)
		}
	}
}
