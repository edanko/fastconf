package configapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/internal/configapi"
	"github.com/edanko/fastconf/layers"
)

func benchLoader(b *testing.B, tenantCount int) *fastconf.Loader {
	b.Helper()
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
	document := map[string]any{"config": map[string]any{"domain": "local", "tenants": tenants}}
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(document)),
	)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() { _ = loader.Close() })
	return loader
}

func benchmarkEndpoint(b *testing.B, target string) {
	loader := benchLoader(b, 1000)
	handler := configapi.New(loader)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	b.ReportAllocs()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			b.Fatalf("status = %d", recorder.Code)
		}
	}
}

func BenchmarkConfigEndpoint(b *testing.B) { benchmarkEndpoint(b, "/v1/config") }

func BenchmarkTenantEndpoint(b *testing.B) { benchmarkEndpoint(b, "/v1/tenants/tenant1") }

func TestHandlerUsesViewWithoutChangingResponses(t *testing.T) {
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{"config": map[string]any{
			"domain": "example.com",
			"tenants": map[string]any{
				"acme": map[string]any{"domain": "acme.example", "database": map[string]any{"host": "db"}},
			},
		}})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })
	handler := configapi.New(loader)

	recorded := func(target string) any {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, recorder.Code)
		}
		var decoded any
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s body: %v", target, err)
		}
		return decoded
	}

	root, err := loader.Map(context.Background())
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	expected, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal root: %v", err)
	}
	var expectedRoot any
	if err := json.Unmarshal(expected, &expectedRoot); err != nil {
		t.Fatalf("unmarshal root: %v", err)
	}
	if got := recorded("/v1/config"); !reflect.DeepEqual(got, expectedRoot) {
		t.Fatalf("config response = %#v, want %#v", got, expectedRoot)
	}

	tenant, err := loader.TenantMap(context.Background(), "acme")
	if err != nil {
		t.Fatalf("TenantMap: %v", err)
	}
	expectedTenantRaw, err := json.Marshal(tenant)
	if err != nil {
		t.Fatalf("marshal tenant: %v", err)
	}
	var expectedTenant any
	if err := json.Unmarshal(expectedTenantRaw, &expectedTenant); err != nil {
		t.Fatalf("unmarshal tenant: %v", err)
	}
	if got := recorded("/v1/tenants/acme"); !reflect.DeepEqual(got, expectedTenant) {
		t.Fatalf("tenant response = %#v, want %#v", got, expectedTenant)
	}
}
