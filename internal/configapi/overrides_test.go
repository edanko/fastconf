package configapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/internal/configapi"
	"github.com/edanko/fastconf/layers"
)

func newOverrideAPI(t *testing.T, token string) *fastconf.Loader {
	t.Helper()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{"config": map[string]any{
			"domain":   "source.example",
			"database": map[string]any{"host": "source-db", "port": 5432},
			"tenants": map[string]any{
				"acme":   map[string]any{"domain": "acme.example"},
				"globex": map[string]any{"domain": "globex.example"},
			},
		}})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })
	return loader
}

func callHandler(handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response body %q: %v", recorder.Body.String(), err)
	}
	return decoded
}

func TestOverrideEndpointLifecycle(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	response := callHandler(handler, http.MethodPut, "/v1/config/feature.flag", `{"value":true}`, "secret")
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	payload := decodeResponse(t, response)
	if payload["path"] != "feature.flag" || payload["value"] != true {
		t.Fatalf("PUT response = %#v", payload)
	}

	config := callHandler(handler, http.MethodGet, "/v1/config", "", "secret")
	var values map[string]any
	if err := json.Unmarshal(config.Body.Bytes(), &values); err != nil {
		t.Fatalf("config body: %v", err)
	}
	feature, ok := values["feature"].(map[string]any)
	if !ok || feature["flag"] != true {
		t.Fatalf("config = %#v", values)
	}

	value := callHandler(handler, http.MethodGet, "/v1/config/feature.flag", "", "secret")
	if value.Code != http.StatusOK || decodeResponse(t, value)["value"] != true {
		t.Fatalf("GET value status=%d body=%s", value.Code, value.Body.String())
	}

	deleted := callHandler(handler, http.MethodDelete, "/v1/config/feature.flag", "", "secret")
	if deleted.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d body=%s", deleted.Code, deleted.Body.String())
	}
	if missing := callHandler(handler, http.MethodGet, "/v1/config/feature.flag", "", "secret"); missing.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d", missing.Code)
	}
	if again := callHandler(handler, http.MethodDelete, "/v1/config/feature.flag", "", "secret"); again.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d", again.Code)
	}
}

func TestTenantOverrideEndpoint(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	response := callHandler(handler, http.MethodPut, "/v1/tenants/acme/config/domain", `{"value":"acme-runtime"}`, "secret")
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	if payload := decodeResponse(t, response); payload["tenant"] != "acme" {
		t.Fatalf("PUT response = %#v", payload)
	}

	acme := callHandler(handler, http.MethodGet, "/v1/tenants/acme", "", "secret")
	var acmeValues map[string]any
	if err := json.Unmarshal(acme.Body.Bytes(), &acmeValues); err != nil {
		t.Fatalf("acme body: %v", err)
	}
	if acmeValues["domain"] != "acme-runtime" {
		t.Fatalf("acme domain = %#v", acmeValues["domain"])
	}

	globex := callHandler(handler, http.MethodGet, "/v1/tenants/globex", "", "secret")
	var globexValues map[string]any
	if err := json.Unmarshal(globex.Body.Bytes(), &globexValues); err != nil {
		t.Fatalf("globex body: %v", err)
	}
	if globexValues["domain"] != "globex.example" {
		t.Fatalf("globex domain = %#v", globexValues["domain"])
	}

	if deleted := callHandler(handler, http.MethodDelete, "/v1/tenants/acme/config/domain", "", "secret"); deleted.Code != http.StatusOK {
		t.Fatalf("tenant DELETE status = %d", deleted.Code)
	}
}

func TestOverrideEndpointGlobalPathPropagatesToTenants(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	if response := callHandler(handler, http.MethodPut, "/v1/config/database/port", `{"value":6000}`, "secret"); response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	values, err := loader.TenantMap(context.Background(), "acme")
	if err != nil {
		t.Fatalf("TenantMap: %v", err)
	}
	database, ok := values["database"].(map[string]any)
	if !ok || database["port"] != float64(6000) {
		t.Fatalf("tenant database = %#v", values["database"])
	}
}

func TestOverrideEndpointsRequireToken(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	withToken := configapi.New(loader, configapi.WithToken("secret"))
	anonymous := configapi.New(loader)

	if response := callHandler(withToken, http.MethodPut, "/v1/config/domain", `{"value":"x"}`, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d", response.Code)
	}
	if response := callHandler(withToken, http.MethodPut, "/v1/config/domain", `{"value":"x"}`, "wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", response.Code)
	}
	if response := callHandler(anonymous, http.MethodPut, "/v1/config/domain", `{"value":"x"}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("tokenless handler status = %d", response.Code)
	}
	if response := callHandler(anonymous, http.MethodGet, "/v1/config", "", ""); response.Code != http.StatusOK {
		t.Fatalf("anonymous read status = %d", response.Code)
	}
}

func TestOverrideEndpointValidatesBody(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	for name, body := range map[string]string{
		"invalid json":  `{"value":`,
		"missing value": `{"other":1}`,
		"unknown field": `{"value":1,"extra":2}`,
	} {
		if response := callHandler(handler, http.MethodPut, "/v1/config/domain", body, "secret"); response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", name, response.Code, response.Body.String())
		}
	}
	if response := callHandler(handler, http.MethodPut, "/v1/config/", `{"value":1}`, "secret"); response.Code != http.StatusBadRequest {
		t.Fatalf("empty path status = %d", response.Code)
	}
}

// plainSource implements configapi.Source without runtime overrides.
type plainSource struct{}

func (plainSource) Map(context.Context) (map[string]any, error) { return map[string]any{}, nil }
func (plainSource) TenantMap(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (plainSource) TenantCodes(context.Context) ([]string, error) { return nil, nil }
func (plainSource) Get(context.Context, string) (any, bool, error) {
	return nil, false, nil
}
func (plainSource) Reload(context.Context) error { return nil }

func TestOverrideEndpointUnsupportedSource(t *testing.T) {
	t.Parallel()
	handler := configapi.New(plainSource{}, configapi.WithToken("secret"))
	if response := callHandler(handler, http.MethodPut, "/v1/config/domain", `{"value":"x"}`, "secret"); response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestOverrideEndpointMethodNotAllowed(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))
	response := callHandler(handler, http.MethodPost, "/v1/config/domain", "", "secret")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
	if allow := response.Header().Get("Allow"); !strings.Contains(allow, "PUT") || !strings.Contains(allow, "DELETE") {
		t.Fatalf("Allow = %q", allow)
	}
}

func TestBatchOverrideEndpoint(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	response := callHandler(handler, http.MethodPatch, "/v1/config", `{"values":{"feature.alpha":true,"database.port":6000}}`, "secret")
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", response.Code, response.Body.String())
	}
	if payload := decodeResponse(t, response); payload["count"] != float64(2) {
		t.Fatalf("response = %#v", payload)
	}
	config := callHandler(handler, http.MethodGet, "/v1/config", "", "secret")
	var values map[string]any
	if err := json.Unmarshal(config.Body.Bytes(), &values); err != nil {
		t.Fatalf("config: %v", err)
	}
	features, ok := values["feature"].(map[string]any)
	if !ok || features["alpha"] != true {
		t.Fatalf("config = %#v", values)
	}

	tenant := callHandler(handler, http.MethodPatch, "/v1/tenants/acme/config", `{"values":{"domain":"acme-batch"}}`, "secret")
	if tenant.Code != http.StatusOK {
		t.Fatalf("tenant PATCH status = %d body=%s", tenant.Code, tenant.Body.String())
	}
	acme := callHandler(handler, http.MethodGet, "/v1/tenants/acme", "", "secret")
	var acmeValues map[string]any
	if err := json.Unmarshal(acme.Body.Bytes(), &acmeValues); err != nil {
		t.Fatalf("acme: %v", err)
	}
	if acmeValues["domain"] != "acme-batch" {
		t.Fatalf("acme domain = %#v", acmeValues["domain"])
	}
}

func TestBatchOverrideEndpointValidation(t *testing.T) {
	t.Parallel()
	loader := newOverrideAPI(t, "secret")
	handler := configapi.New(loader, configapi.WithToken("secret"))

	for name, body := range map[string]string{
		"missing values": `{}`,
		"unknown field":  `{"values":{},"extra":1}`,
		"invalid path":   `{"values":{"a..b":1}}`,
	} {
		if response := callHandler(handler, http.MethodPatch, "/v1/config", body, "secret"); response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", name, response.Code, response.Body.String())
		}
	}
	if response := callHandler(configapi.New(loader), http.MethodPatch, "/v1/config", `{"values":{}}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("tokenless PATCH status = %d", response.Code)
	}
	if response := callHandler(configapi.New(plainSource{}, configapi.WithToken("secret")), http.MethodPatch, "/v1/config", `{"values":{}}`, "secret"); response.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported source status = %d", response.Code)
	}
	if response := callHandler(handler, http.MethodPut, "/v1/config", "", "secret"); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /v1/config status = %d", response.Code)
	}
}
