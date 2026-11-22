package configapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edanko/fastconf"
)

type fakeSource struct {
	config     map[string]any
	tenants    map[string]map[string]any
	codes      []string
	mapErr     error
	getErr     error
	tenantErr  error
	codesErr   error
	reloadErr  error
	panicOnMap bool
	reloads    int
}

func (f *fakeSource) Map(context.Context) (map[string]any, error) {
	if f.panicOnMap {
		panic("boom")
	}
	return f.config, f.mapErr
}

func (f *fakeSource) TenantMap(_ context.Context, code string) (map[string]any, error) {
	if f.tenantErr != nil {
		return nil, f.tenantErr
	}
	if tenant, ok := f.tenants[code]; ok {
		return tenant, nil
	}
	return nil, errors.Join(fastconf.ErrTenantNotFound, errors.New("missing"))
}

func (f *fakeSource) TenantCodes(context.Context) ([]string, error) {
	return f.codes, f.codesErr
}

func (f *fakeSource) Get(_ context.Context, path string) (any, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	var current any = f.config
	for segment := range strings.SplitSeq(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		current, ok = object[segment]
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}

func (f *fakeSource) Reload(context.Context) error {
	f.reloads++
	return f.reloadErr
}

func newTestSource() *fakeSource {
	return &fakeSource{
		config: map[string]any{
			"domain": "example.com",
			"database": map[string]any{
				"host": "localhost",
			},
		},
		tenants: map[string]map[string]any{
			"acme": {"tenantCode": "acme", "domain": "acme.example.com"},
		},
		codes: []string{"acme"},
	}
}

func request(t *testing.T, handler http.Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestEndpoints(t *testing.T) {
	t.Parallel()
	handler := New(newTestSource())

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantBody   string
	}{
		{"health", http.MethodGet, "/healthz", http.StatusOK, `"status":"ok"`},
		{"config", http.MethodGet, "/v1/config", http.StatusOK, `"domain":"example.com"`},
		{"value", http.MethodGet, "/v1/config/database/host", http.StatusOK, `"value":"localhost"`},
		{"value missing", http.MethodGet, "/v1/config/database/port", http.StatusNotFound, `"error"`},
		{"tenants", http.MethodGet, "/v1/tenants", http.StatusOK, `"acme"`},
		{"tenant", http.MethodGet, "/v1/tenants/acme", http.StatusOK, `"tenantCode":"acme"`},
		{"tenant missing", http.MethodGet, "/v1/tenants/unknown", http.StatusNotFound, `"tenant not found"`},
		{"reload", http.MethodPost, "/v1/reload", http.StatusOK, `"status":"reloaded"`},
		{"method not allowed", http.MethodPost, "/v1/config", http.StatusMethodNotAllowed, `"error"`},
		{"unknown endpoint", http.MethodGet, "/v1/unknown", http.StatusNotFound, `"error"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := request(t, handler, test.method, test.target, "")
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, test.wantStatus, recorder.Body)
			}
			if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("content type = %q", contentType)
			}
			if body := recorder.Body.String(); !strings.Contains(body, test.wantBody) {
				t.Fatalf("body = %s, want substring %s", body, test.wantBody)
			}
		})
	}
}

func TestAuthentication(t *testing.T) {
	t.Parallel()
	handler := New(newTestSource(), WithToken("secret"))

	if recorder := request(t, handler, http.MethodGet, "/healthz", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", recorder.Code)
	}
	if recorder := request(t, handler, http.MethodGet, "/healthz", "wrong"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d", recorder.Code)
	}
	if recorder := request(t, handler, http.MethodGet, "/healthz", "secret"); recorder.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d", recorder.Code)
	}

	open := New(newTestSource())
	if recorder := request(t, open, http.MethodGet, "/healthz", ""); recorder.Code != http.StatusOK {
		t.Fatalf("open handler: status = %d", recorder.Code)
	}
}

func TestPanicRecovery(t *testing.T) {
	t.Parallel()
	source := newTestSource()
	source.panicOnMap = true
	recorder := request(t, New(source), http.MethodGet, "/v1/config", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "internal server error" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestSourceErrors(t *testing.T) {
	t.Parallel()
	source := newTestSource()
	source.mapErr = errors.New("map failed")
	if recorder := request(t, New(source), http.MethodGet, "/v1/config", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("map error status = %d", recorder.Code)
	}

	source = newTestSource()
	source.getErr = errors.New("get failed")
	if recorder := request(t, New(source), http.MethodGet, "/v1/config/domain", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("get error status = %d", recorder.Code)
	}

	source = newTestSource()
	source.codesErr = errors.New("codes failed")
	if recorder := request(t, New(source), http.MethodGet, "/v1/tenants", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("codes error status = %d", recorder.Code)
	}

	source = newTestSource()
	source.tenantErr = errors.New("tenant failed")
	if recorder := request(t, New(source), http.MethodGet, "/v1/tenants/acme", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("tenant error status = %d", recorder.Code)
	}

	source = newTestSource()
	source.reloadErr = errors.New("reload failed")
	if recorder := request(t, New(source), http.MethodPost, "/v1/reload", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("reload error status = %d", recorder.Code)
	}
}

func TestReloadIsForwarded(t *testing.T) {
	t.Parallel()
	source := newTestSource()
	handler := New(source)
	for range 2 {
		if recorder := request(t, handler, http.MethodPost, "/v1/reload", ""); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	}
	if source.reloads != 2 {
		t.Fatalf("reloads = %d, want 2", source.reloads)
	}
}
