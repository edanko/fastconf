package decode_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/edanko/fastconf/internal/decode"
	"github.com/edanko/fastconf/internal/value"
)

// The benchmarks below document the cost of the decode path. The JSON
// benchmarks are a local encoding/json baseline for reference only: they parse
// raw JSON bytes, so they measure a different amount of work. They are not part
// of the library and exist to make the decode numbers interpretable.

const benchmarkTenantCount = 1001

type benchmarkPublicURIs struct {
	AdminAddress  string
	AdminBasepath string
	AdminURI      string
}

type benchmarkTenant struct {
	TenantCode string
	Domain     string
	PublicURIs benchmarkPublicURIs
}

type benchmarkScalar struct {
	Name    string
	Workers int
	Debug   bool
	Ratio   float64
	Timeout time.Duration
}

// benchmarkTenantDocument builds the in-memory document for one tenant.
func benchmarkTenantDocument(index int) map[string]any {
	return map[string]any{
		"tenantCode": fmt.Sprintf("tenant%d", index),
		"domain":     fmt.Sprintf("tenant%d.example", index),
		"publicUris": map[string]any{
			"adminAddress":  fmt.Sprintf("https://tenant%d.example", index),
			"adminBasepath": fmt.Sprintf("/admin/tenant%d", index),
			"adminUri":      fmt.Sprintf("https://tenant%d.example/admin", index),
		},
	}
}

// benchmarkTenantsDocument builds the document holding every benchmark tenant.
func benchmarkTenantsDocument() map[string]any {
	tenants := make(map[string]any, benchmarkTenantCount)
	for i := 1; i <= benchmarkTenantCount; i++ {
		tenants[fmt.Sprintf("tenant%d", i)] = benchmarkTenantDocument(i)
	}
	return tenants
}

// benchmarkScalarDocument builds a document for a struct of scalars only.
func benchmarkScalarDocument() map[string]any {
	return map[string]any{
		"name":    "worker",
		"workers": 8,
		"debug":   true,
		"ratio":   0.5,
		"timeout": int64(5 * time.Second),
	}
}

func BenchmarkDecodeTenantsMap(b *testing.B) {
	src := value.From(benchmarkTenantsDocument())
	decoder := decode.New(decode.Config{})

	var sink map[string]benchmarkTenant
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]benchmarkTenant
		if err := decoder.Decode(&src, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}

func BenchmarkDecodeTenantStruct(b *testing.B) {
	src := value.From(benchmarkTenantDocument(7))
	decoder := decode.New(decode.Config{})

	var sink benchmarkTenant
	b.ReportAllocs()
	for b.Loop() {
		var out benchmarkTenant
		if err := decoder.Decode(&src, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}

func BenchmarkDecodeScalarStruct(b *testing.B) {
	src := value.From(benchmarkScalarDocument())
	decoder := decode.New(decode.Config{})

	var sink benchmarkScalar
	b.ReportAllocs()
	for b.Loop() {
		var out benchmarkScalar
		if err := decoder.Decode(&src, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}

// BenchmarkJSONTenantsMapBaseline is a local encoding/json baseline for the
// thousand-tenant document. It includes JSON parsing.
func BenchmarkJSONTenantsMapBaseline(b *testing.B) {
	data, err := json.Marshal(benchmarkTenantsDocument())
	if err != nil {
		b.Fatal(err)
	}

	var sink map[string]benchmarkTenant
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]benchmarkTenant
		if err := json.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}

// BenchmarkJSONTenantStructBaseline is a local encoding/json baseline for one
// tenant object. It includes JSON parsing.
func BenchmarkJSONTenantStructBaseline(b *testing.B) {
	data, err := json.Marshal(benchmarkTenantDocument(7))
	if err != nil {
		b.Fatal(err)
	}

	var sink benchmarkTenant
	b.ReportAllocs()
	for b.Loop() {
		var out benchmarkTenant
		if err := json.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}

// BenchmarkJSONScalarStructBaseline is a local encoding/json baseline for a
// struct of scalars. It includes JSON parsing.
func BenchmarkJSONScalarStructBaseline(b *testing.B) {
	data, err := json.Marshal(benchmarkScalarDocument())
	if err != nil {
		b.Fatal(err)
	}

	var sink benchmarkScalar
	b.ReportAllocs()
	for b.Loop() {
		var out benchmarkScalar
		if err := json.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
		sink = out
	}
	_ = sink
}
