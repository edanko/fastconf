package fastconf_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/edanko/fastconf"
)

func benchmarkOverrideLoader(b *testing.B) *fastconf.Loader {
	b.Helper()
	loader := benchmarkLoader(b, 1000)
	if err := loader.Reload(context.Background()); err != nil {
		b.Fatalf("Reload: %v", err)
	}
	return loader
}

func BenchmarkSetOverride(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	b.ReportAllocs()
	for b.Loop() {
		if err := loader.SetOverride("feature.flag", true); err != nil {
			b.Fatalf("SetOverride: %v", err)
		}
	}
}

func BenchmarkSetTenantOverride(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	b.ReportAllocs()
	for b.Loop() {
		if err := loader.SetTenantOverride("tenant1", "database.host", "db.internal"); err != nil {
			b.Fatalf("SetTenantOverride: %v", err)
		}
	}
}

func BenchmarkDeleteOverride(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	if err := loader.SetOverride("feature.flag", true); err != nil {
		b.Fatalf("SetOverride: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		if err := loader.SetOverride("feature.flag", true); err != nil {
			b.Fatalf("SetOverride: %v", err)
		}
		b.StartTimer()
		if _, err := loader.DeleteOverride("feature.flag"); err != nil {
			b.Fatalf("DeleteOverride: %v", err)
		}
	}
}

func BenchmarkDecodeStructWithOverrides(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	if err := loader.SetOverride("feature.flag", true); err != nil {
		b.Fatalf("SetOverride: %v", err)
	}
	var decoded benchmarkConfig
	b.ReportAllocs()
	for b.Loop() {
		if err := loader.Decode(context.Background(), &decoded); err != nil {
			b.Fatalf("Decode: %v", err)
		}
	}
}

func BenchmarkMapWithOverrides(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	if err := loader.SetOverride("feature.flag", true); err != nil {
		b.Fatalf("SetOverride: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := loader.Map(context.Background()); err != nil {
			b.Fatalf("Map: %v", err)
		}
	}
}

func batchOverrideValues() map[string]any {
	values := make(map[string]any, 10)
	for index := range 10 {
		values[fmt.Sprintf("batch.value%d", index)] = index
	}
	return values
}

func BenchmarkSetOverridesBatch(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	values := batchOverrideValues()
	b.ReportAllocs()
	for b.Loop() {
		if err := loader.SetOverrides(values); err != nil {
			b.Fatalf("SetOverrides: %v", err)
		}
	}
}

func BenchmarkSetOverrideIndividual(b *testing.B) {
	loader := benchmarkOverrideLoader(b)
	values := batchOverrideValues()
	b.ReportAllocs()
	for b.Loop() {
		for configPath, value := range values {
			if err := loader.SetOverride(configPath, value); err != nil {
				b.Fatalf("SetOverride: %v", err)
			}
		}
	}
}
