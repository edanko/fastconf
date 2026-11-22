package layers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edanko/fastconf/auth"
	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/retry"
)

func TestHTTPLoadMergesDocument(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote","port":5432},"feature":{"enabled":true}}`)
	}))
	defer server.Close()

	values := map[string]any{
		"config": map[string]any{
			"database": map[string]any{"host": "local", "name": "app"},
		},
	}
	result, err := NewHTTP(server.URL).Load(context.Background(), values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "remote" {
		t.Fatalf("database.host = %q, want remote", got)
	}
	if got := httpTestString(t, result, "config", "database", "name"); got != "app" {
		t.Fatalf("database.name = %q, want the incoming value to survive", got)
	}
	if result["config"].(map[string]any)["database"].(map[string]any)["port"] != float64(5432) {
		t.Fatalf("database.port = %v, want 5432", result["config"].(map[string]any)["database"].(map[string]any)["port"])
	}
}

func TestHTTPBareDocumentIsWrapped(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote"}}`)
	}))
	defer server.Close()

	result, err := NewHTTP(server.URL).Load(context.Background(), map[string]any{
		"database": map[string]any{"host": "local"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := result["config"].(map[string]any); !ok {
		t.Fatalf("result has no config root: %v", result)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "remote" {
		t.Fatalf("database.host = %q, want remote", got)
	}
}

func TestHTTPOverridesEarlierSources(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote"}}`)
	}))
	defer server.Close()

	chain := NewChain(
		NewInMemory(map[string]any{"database": map[string]any{"host": "local", "name": "app"}}),
		NewHTTP(server.URL),
	)
	result, err := chain.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "remote" {
		t.Fatalf("database.host = %q, want the HTTP value to win", got)
	}
	if got := httpTestString(t, result, "config", "database", "name"); got != "app" {
		t.Fatalf("database.name = %q, want the earlier value to survive", got)
	}
}

func TestHTTPRetriesFailedAttempts(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) < 3 {
			writer.WriteHeader(http.StatusInternalServerError)
			io.WriteString(writer, "transient")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"retried"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL, WithHTTPRetry(retry.Policy{
		MaxAttempts:     3,
		InitialInterval: time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		Multiplier:      2,
	}))
	result, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "retried" {
		t.Fatalf("database.host = %q, want retried", got)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
}

func TestHTTPExhaustsRetriesWithStatusError(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
		io.WriteString(writer, "upstream is down")
	}))
	defer server.Close()

	source := NewHTTP(server.URL, WithHTTPRetry(retry.Policy{
		MaxAttempts:     2,
		InitialInterval: time.Millisecond,
	}))
	_, err := source.Load(context.Background(), nil)
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want *HTTPStatusError", err)
	}
	if statusErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want %d", statusErr.StatusCode, http.StatusBadGateway)
	}
	if !strings.Contains(statusErr.Excerpt, "upstream is down") {
		t.Fatalf("Excerpt = %q, want the response body", statusErr.Excerpt)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want MaxAttempts = 2", got)
	}
}

func TestHTTPErrorExcerptIsBounded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(writer, strings.Repeat("e", 4096)+"END-OF-BODY")
	}))
	defer server.Close()

	source := NewHTTP(server.URL, WithHTTPRetry(retry.Policy{MaxAttempts: 1}))
	_, err := source.Load(context.Background(), nil)
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	message := err.Error()
	if !strings.Contains(message, "503 Service Unavailable") {
		t.Fatalf("error = %q, want the status line", message)
	}
	if strings.Contains(message, "END-OF-BODY") {
		t.Fatalf("error contains the whole body: %q", message)
	}
	if len(message) > 1024 {
		t.Fatalf("error length = %d, want a bounded excerpt", len(message))
	}
}

func TestHTTPStaleCacheFallback(t *testing.T) {
	t.Parallel()

	cacheDir := filepath.Join(t.TempDir(), "cache", "nested")
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if failing.Load() {
			writer.WriteHeader(http.StatusInternalServerError)
			io.WriteString(writer, "server is unavailable")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"cached"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL+"/config.json",
		WithHTTPCacheDir(cacheDir),
		WithHTTPStaleOnError(true),
		WithHTTPRetry(retry.Policy{MaxAttempts: 1}),
	)

	first, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if got := httpTestString(t, first, "config", "database", "host"); got != "cached" {
		t.Fatalf("database.host = %q, want cached", got)
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(entries))
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("cache contains a temporary file %q", entry.Name())
		}
		if !strings.HasSuffix(entry.Name(), httpCacheSuffix) {
			t.Fatalf("cache file %q does not have the expected suffix", entry.Name())
		}
	}

	failing.Store(true)
	second, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("second Load with stale fallback: %v", err)
	}
	if got := httpTestString(t, second, "config", "database", "host"); got != "cached" {
		t.Fatalf("stale database.host = %q, want cached", got)
	}
}

func TestHTTPStaleCacheDisabledByDefault(t *testing.T) {
	t.Parallel()

	cacheDir := filepath.Join(t.TempDir(), "cache")
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if failing.Load() {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"cached"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL,
		WithHTTPCacheDir(cacheDir),
		WithHTTPRetry(retry.Policy{MaxAttempts: 1}),
	)
	if _, err := source.Load(context.Background(), nil); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	failing.Store(true)
	if _, err := source.Load(context.Background(), nil); err == nil {
		t.Fatal("Load succeeded, want the HTTP error because stale fallback is disabled")
	}
}

func TestHTTPCacheWriteFailureDoesNotFailLoad(t *testing.T) {
	t.Parallel()

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL, WithHTTPCacheDir(blocked), WithHTTPStaleOnError(true))
	result, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "remote" {
		t.Fatalf("database.host = %q, want remote", got)
	}
}

func TestHTTPAuthAndHeaders(t *testing.T) {
	t.Parallel()

	type received struct {
		authorization string
		tenant        string
	}
	requests := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- received{
			authorization: request.Header.Get("Authorization"),
			tenant:        request.Header.Get("X-Tenant"),
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL,
		WithHTTPAuth(auth.Bearer("secret-token")),
		WithHTTPHeaders(map[string]string{"X-Tenant": "acme"}),
	)
	if _, err := source.Load(context.Background(), nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	select {
	case got := <-requests:
		if got.authorization != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want the bearer token", got.authorization)
		}
		if got.tenant != "acme" {
			t.Fatalf("X-Tenant = %q, want acme", got.tenant)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server was not called")
	}
}

func TestHTTPConditionalRequest(t *testing.T) {
	t.Parallel()

	headers := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		modifiedSince := request.Header.Get("If-Modified-Since")
		headers <- modifiedSince
		if modifiedSince != "" {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"database":{"host":"remote"}}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL + "/config")
	modified := time.Date(2026, time.March, 5, 6, 7, 8, 0, time.UTC)
	ctx := WithModifiedAt(context.Background(), &modified)

	values := map[string]any{
		"config": map[string]any{"database": map[string]any{"host": "local"}},
	}
	result, err := source.Load(ctx, values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "local" {
		t.Fatalf("database.host = %q, want the unchanged local value", got)
	}
	if got := <-headers; got != "Thu, 05 Mar 2026 06:07:08 GMT" {
		t.Fatalf("If-Modified-Since = %q", got)
	}

	// Without a modification time the request is unconditional and receives
	// the document.
	result, err = source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("unconditional Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "remote" {
		t.Fatalf("database.host = %q, want remote", got)
	}
	if got := <-headers; got != "" {
		t.Fatalf("unconditional If-Modified-Since = %q, want empty", got)
	}
}

func TestHTTPModifiedAtContext(t *testing.T) {
	t.Parallel()

	if _, ok := ModifiedAt(context.Background()); ok {
		t.Fatal("bare context carries a modification time")
	}
	modified := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	ctx := WithModifiedAt(context.Background(), &modified)
	got, ok := ModifiedAt(ctx)
	if !ok {
		t.Fatal("context does not carry a modification time")
	}
	if !got.Equal(modified) {
		t.Fatalf("modification time = %v, want %v", got, modified)
	}
	modified = modified.Add(time.Hour)
	if got, _ := ModifiedAt(ctx); !got.Equal(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)) {
		t.Fatalf("context time changed with the caller value: %v", got)
	}
	var nilContext context.Context
	if ctx := WithModifiedAt(nilContext, nil); ctx == nil {
		t.Fatal("WithModifiedAt returned a nil context")
	}
}

func TestHTTPDecodeByContentType(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		io.WriteString(writer, "database:\n  host: yaml-host\n")
	}))
	defer server.Close()

	result, err := NewHTTP(server.URL+"/config").Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "yaml-host" {
		t.Fatalf("database.host = %q, want yaml-host", got)
	}
}

func TestHTTPDecodeByURLExtension(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, "database:\n  host: yaml-host\n")
	}))
	defer server.Close()

	result, err := NewHTTP(server.URL+"/config.yaml").Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "yaml-host" {
		t.Fatalf("database.host = %q, want yaml-host", got)
	}
}

func TestHTTPDecodeJSONFallback(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, `{"database":{"host":"json-host"}}`)
	}))
	defer server.Close()

	result, err := NewHTTP(server.URL+"/config").Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "json-host" {
		t.Fatalf("database.host = %q, want json-host", got)
	}
}

func TestHTTPExplicitEncodingWins(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, "database:\n  host: yaml-host\n")
	}))
	defer server.Close()

	source := NewHTTP(server.URL+"/config.json", WithHTTPEncoding(encoding.YAML))
	result, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := httpTestString(t, result, "config", "database", "host"); got != "yaml-host" {
		t.Fatalf("database.host = %q, want yaml-host", got)
	}
}

func TestHTTPMethodOption(t *testing.T) {
	t.Parallel()

	methods := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods <- request.Method
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	if _, err := NewHTTP(server.URL, WithHTTPMethod("post")).Load(context.Background(), nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := <-methods; got != http.MethodPost {
		t.Fatalf("method = %q, want POST", got)
	}
}

func TestHTTPTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(80 * time.Millisecond)
		io.WriteString(writer, `{}`)
	}))
	defer server.Close()

	source := NewHTTP(server.URL,
		WithHTTPTimeout(20*time.Millisecond),
		WithHTTPRetry(retry.Policy{MaxAttempts: 1}),
	)
	started := time.Now()
	_, err := source.Load(context.Background(), nil)
	if err == nil {
		t.Fatal("Load succeeded, want a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Load took %v, want a prompt timeout", elapsed)
	}
}

func TestHTTPInvalidURL(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{"", "   ", "ftp://example.com/config", "://missing-scheme", "/relative/path"} {
		source := NewHTTP(rawURL)
		if _, err := source.Load(context.Background(), nil); err == nil {
			t.Fatalf("Load accepted url %q", rawURL)
		}
	}
}

// httpTestString reads a nested string value from a configuration document.
func httpTestString(t *testing.T, document map[string]any, path ...string) string {
	t.Helper()
	var current any = document
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("value at %v is not an object: %v", path, current)
		}
		value, ok := object[key]
		if !ok {
			t.Fatalf("missing key %q in path %v", key, path)
		}
		current = value
	}
	value, ok := current.(string)
	if !ok {
		t.Fatalf("value at %v is not a string: %v", path, current)
	}
	return value
}
