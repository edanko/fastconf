package fastconf

import (
	"fmt"
	"testing"
)

func benchmarkSnapshotDocument(tenantCount int) map[string]any {
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
	return map[string]any{"config": map[string]any{"domain": "local", "tenants": tenants}}
}

func BenchmarkNewSnapshot(b *testing.B) {
	document := benchmarkSnapshotDocument(1000)
	b.ReportAllocs()
	for b.Loop() {
		_ = newSnapshot(document)
	}
}
