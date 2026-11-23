package webui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/internal/webui"
	"github.com/edanko/fastconf/layers"
)

func newLoader(t *testing.T) *fastconf.Loader {
	t.Helper()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(layers.NewInMemory(map[string]any{"config": map[string]any{
			"domain":    "console.example",
			"database":  map[string]any{"host": "db.internal", "port": 5432},
			"tenants":   map[string]any{"acme": map[string]any{"domain": "acme.example"}},
			"longValue": strings.Repeat("x", 140),
			"markup":    "<script>alert(1)</script>",
		}})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })
	return loader
}

func newHandler(t *testing.T, loader *fastconf.Loader, options ...webui.Option) http.Handler {
	t.Helper()
	handler, err := webui.New(loader, options...)
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}
	return handler
}

func TestPageAndScope(t *testing.T) {
	t.Parallel()
	loader := newLoader(t)
	handler := newHandler(t, loader)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("page status = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"Root configuration", "htmx.min.js", "tenant-list", "New override"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("configuration value was not HTML-escaped")
	}
	if got := page.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("missing content security policy")
	}

	scope := httptest.NewRecorder()
	handler.ServeHTTP(scope, httptest.NewRequest(http.MethodGet, "/ui/scope?tenant=acme", nil))
	if scope.Code != http.StatusOK {
		t.Fatalf("scope status = %d", scope.Code)
	}
	if !strings.Contains(scope.Body.String(), "acme") {
		t.Fatal("tenant scope missing")
	}
}

func TestOverrideFlowAndCSRF(t *testing.T) {
	t.Parallel()
	loader := newLoader(t)
	handler := newHandler(t, loader)

	form := url.Values{"tenant": {""}, "path": {"features.newBilling"}, "value": {"true"}}
	blocked := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/ui/overrides", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("request without HX-Request status = %d", blocked.Code)
	}

	allowed := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/ui/overrides", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusOK {
		t.Fatalf("override status = %d", allowed.Code)
	}
	value, found, err := loader.Get(context.Background(), "features.newBilling")
	if err != nil || !found || value != true {
		t.Fatalf("override value = %#v, %v, %v", value, found, err)
	}
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	loader := newLoader(t)
	handler := newHandler(t, loader, webui.WithToken("secret"))

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	if anonymous.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}

	request := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	request.SetBasicAuth("ui", "secret")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d", authorized.Code)
	}
}
