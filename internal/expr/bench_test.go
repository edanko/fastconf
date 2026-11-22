package expr

import (
	"context"
	"strconv"
	"testing"
)

// benchmarkTemplate is a conditional template with quoting, comparisons,
// nested lookups and a nested conditional, used by BenchmarkCompileCached.
const benchmarkTemplate = `#{ ${environment} == "production" ? "prod-${database.host}" : "local" }`

// BenchmarkCompileCached measures compiling a template that is already in the
// cache. The compiled result is kept alive so the call is not optimized away.
func BenchmarkCompileCached(b *testing.B) {
	if _, err := Compile(benchmarkTemplate); err != nil {
		b.Fatalf("Compile(%q) error = %v", benchmarkTemplate, err)
	}
	b.ReportAllocs()
	var sink *Template
	for b.Loop() {
		template, err := Compile(benchmarkTemplate)
		if err != nil {
			b.Fatalf("Compile(%q) error = %v", benchmarkTemplate, err)
		}
		sink = template
	}
	_ = sink
}

// BenchmarkResolveNoMarkers measures Resolve on a document without any
// placeholder, which exercises the fast path.
func BenchmarkResolveNoMarkers(b *testing.B) {
	root := make(map[string]any, 100)
	for i := range 100 {
		root["key"+strconv.Itoa(i)] = "value-" + strconv.Itoa(i)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := Resolve(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolveTypical measures Resolve on a small but realistic document
// with lookups, defaults, conditionals and list splicing. Building the document
// is excluded from the measurement.
func BenchmarkResolveTypical(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		root := typicalDocument()
		b.StartTimer()
		if err := Resolve(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolveLargeDocument measures Resolve on a document with 1000
// tenants, each holding several placeholders. Building the document is excluded
// from the measurement.
func BenchmarkResolveLargeDocument(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		root := largeDocument(1000)
		b.StartTimer()
		if err := Resolve(ctx, root); err != nil {
			b.Fatal(err)
		}
	}
}

// typicalDocument builds a small realistic configuration root.
func typicalDocument() map[string]any {
	return map[string]any{
		"name":        "billing",
		"environment": "production",
		"database": map[string]any{
			"host": "db.internal",
			"port": 5432,
			"dsn":  "postgres://${name}@${database.host}:${database.port}/billing",
		},
		"cache": map[string]any{
			"addr": "${database.host}:6379",
			"ttl":  "${cache.ttlSeconds:30}",
		},
		"features": []any{"metrics", "tracing"},
		"servers": []any{
			map[string]any{"url": "https://${database.host}/${name}", "weight": 10},
			map[string]any{
				"url":    "https://${environment}.${database.host}",
				"weight": "#{ ${database.port} > 1024 ? 20 : 5 }",
			},
		},
		"labels":  []any{"app", "*${features}"},
		"enabled": `#{ ${environment} == "production" and ${database.port} == 5432 ? true : false }`,
	}
}

// largeDocument builds a configuration root with tenantCount tenant objects.
func largeDocument(tenantCount int) map[string]any {
	root := map[string]any{
		"domain":      "example.test",
		"environment": "production",
		"defaults": map[string]any{
			"retries": 3,
			"timeout": "5s",
			"hosts":   []any{"${environment}.${domain}", "static"},
		},
	}
	tenants := make(map[string]any, tenantCount)
	for i := range tenantCount {
		code := "tenant" + strconv.Itoa(i)
		tenants[code] = map[string]any{
			"database": map[string]any{
				"host": "${domain}",
				"port": 5432,
				"dsn":  "postgres://${domain}/app",
			},
			"url":     "https://${domain}/" + strconv.Itoa(i),
			"retries": "${defaults.retries}",
			"enabled": `#{ ${environment} == "production" ? 1 : 0 }`,
			"hosts":   []any{"${domain}", "*${defaults.hosts}"},
		}
	}
	root["tenants"] = tenants
	return root
}
