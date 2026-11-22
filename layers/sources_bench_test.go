package layers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/layers"
)

// benchmarkTenants is the tenant count of the benchmark document. The document
// mirrors a production shaped configuration: subsystem defaults, an environment
// override and one complete override block per tenant.
const benchmarkTenants = 1000

func BenchmarkDecodeYAML1000Tenants(b *testing.B) {
	data := benchmarkDocumentData(b, encoding.YAML)
	benchmarkDecode(b, data, encoding.YAML)
}

func BenchmarkDecodeJSON1000Tenants(b *testing.B) {
	data := benchmarkDocumentData(b, encoding.JSON)
	benchmarkDecode(b, data, encoding.JSON)
}

func BenchmarkFileLoadYAML1000Tenants(b *testing.B) {
	data := benchmarkDocumentData(b, encoding.YAML)
	benchmarkFileLoad(b, "config.yaml", data)
}

func BenchmarkFileLoadJSON1000Tenants(b *testing.B) {
	data := benchmarkDocumentData(b, encoding.JSON)
	benchmarkFileLoad(b, "config.json", data)
}

// benchmarkDecode measures parsing and normalization without file access.
func benchmarkDecode(b *testing.B, data []byte, enc encoding.Encoding) {
	b.Helper()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := encoding.Decode(bytes.NewReader(data), enc); err != nil {
			b.Fatalf("Decode() error = %v", err)
		}
	}
}

// benchmarkFileLoad measures the whole source pipeline: reading the file,
// decoding it, normalizing it and merging it into a fresh document.
func benchmarkFileLoad(b *testing.B, name string, data []byte) {
	b.Helper()
	path := filepath.Join(b.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		b.Fatalf("write benchmark fixture: %v", err)
	}
	source := layers.NewFile(path)
	ctx := context.Background()

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := source.Load(ctx, map[string]any{}); err != nil {
			b.Fatalf("Load() error = %v", err)
		}
	}
}

// benchmarkDocumentData builds the benchmark document in the requested format.
func benchmarkDocumentData(tb testing.TB, enc encoding.Encoding) []byte {
	tb.Helper()
	document := benchmarkDocument()

	var (
		data []byte
		err  error
	)
	switch enc {
	case encoding.YAML:
		data, err = yaml.Marshal(document)
	case encoding.JSON:
		data, err = json.Marshal(document)
	default:
		tb.Fatalf("unsupported benchmark encoding %q", enc)
	}
	if err != nil {
		tb.Fatalf("marshal benchmark document: %v", err)
	}
	return data
}

// benchmarkDocument returns the benchmark configuration document.
func benchmarkDocument() map[string]any {
	tenants := make(map[string]any, benchmarkTenants)
	for index := range benchmarkTenants {
		code := fmt.Sprintf("tenant-%04d", index)
		tenants[code] = map[string]any{
			"tenantCode": code,
			"database": map[string]any{
				"host": fmt.Sprintf("db-%s.example.test", code),
				"port": 5432,
				"pool": map[string]any{
					"maxOpen": 10,
					"maxIdle": 4,
				},
			},
			"features": map[string]any{
				"invoices": true,
				"reports":  index%2 == 0,
				"exports":  true,
			},
			"quotas": map[string]any{
				"requestsPerSecond": 100 + index%50,
				"burst":             200,
			},
			"labels": map[string]any{
				"region": "eu-central-1",
				"tier":   "gold",
			},
		}
	}

	return map[string]any{
		"config": map[string]any{
			"application": "billing",
			"subsystems": map[string]any{
				"payments": map[string]any{
					"timeout":  "5s",
					"retries":  3,
					"jitter":   0.2,
					"enabled":  true,
					"fallback": nil,
					"endpoints": []any{
						map[string]any{"name": "primary", "url": "https://payments.example.test"},
						map[string]any{"name": "fallback", "url": "https://payments-fallback.example.test"},
					},
				},
			},
			"environments": map[string]any{
				"production": map[string]any{
					"database": map[string]any{
						"host": "db.production.example.test",
						"port": 5432,
						"pool": map[string]any{"maxOpen": 50, "maxIdle": 10},
					},
					"logging": map[string]any{"level": "info", "format": "json"},
				},
			},
			"tenants": tenants,
		},
	}
}
