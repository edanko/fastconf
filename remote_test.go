package fastconf_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/retry"
)

func TestBootstrapRemoteSource(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if token := request.Header.Get("Authorization"); token != "Bearer secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"config": {
				"database": {"host": "remote-db", "port": 7000},
				"tenants": {"acme": {"domain": "remote-acme.example"}}
			}
		}`))
	}))
	defer server.Close()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte("config:\n  database:\n    host: file-db\n"), 0o644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	bootstrap := fmt.Sprintf("config:\n  remote:\n    url: %s\n    timeout: 5s\n    auth:\n      type: bearer\n      token: secret\n", server.URL)
	if err := os.WriteFile(filepath.Join(directory, "bootstrap.yml"), []byte(bootstrap), 0o644); err != nil {
		t.Fatalf("WriteFile bootstrap: %v", err)
	}

	loader, err := fastconf.New(
		fastconf.WithConfigPath(directory),
		fastconf.WithBootstrapPath(""),
		fastconf.WithoutEnvironmentVariables(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer loader.Close()

	var config applicationConfig
	if err := loader.Decode(context.Background(), &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Database.Host != "remote-db" || config.Database.Port != 7000 {
		t.Fatalf("remote values were not merged: %#v", config.Database)
	}

	var tenant tenantConfig
	if err := loader.DecodeTenant(context.Background(), "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.Domain != "remote-acme.example" {
		t.Fatalf("remote tenant = %#v", tenant)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}

	// A reload fetches the remote document again.
	if err := loader.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests after reload = %d, want 2", requests.Load())
	}
}

func TestRemoteRetry(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"config": {"domain": "retried.example"}}`))
	}))
	defer server.Close()

	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithRemote(fastconf.RemoteConfig{
			URL: server.URL,
			Retry: &retry.Policy{
				MaxAttempts:     4,
				InitialInterval: time.Millisecond,
				MaxInterval:     5 * time.Millisecond,
				Multiplier:      2,
			},
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer loader.Close()

	var config applicationConfig
	if err := loader.Decode(context.Background(), &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "retried.example" {
		t.Fatalf("domain = %q", config.Domain)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestRemoteStaleCache(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"config": {"domain": "cached.example"}}`))
	}))
	defer server.Close()

	cacheDirectory := t.TempDir()
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithRemote(fastconf.RemoteConfig{
			URL:      server.URL,
			CacheDir: cacheDirectory,
			UseStale: true,
			Retry:    &retry.Policy{MaxAttempts: 1},
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer loader.Close()

	ctx := context.Background()
	var config applicationConfig
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "cached.example" {
		t.Fatalf("domain = %q", config.Domain)
	}

	fail.Store(true)
	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("Reload with stale cache: %v", err)
	}
	config = applicationConfig{}
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode after stale reload: %v", err)
	}
	if config.Domain != "cached.example" {
		t.Fatalf("stale domain = %q", config.Domain)
	}
}

func TestLoadBootstrap(t *testing.T) {
	directory := t.TempDir()
	bootstrap := "config:\n  remote:\n    url: https://config.example.com/config\n    timeout: 3s\n    auth:\n      type: bearer\n      token: file-token\n"
	if err := os.WriteFile(filepath.Join(directory, "bootstrap.yml"), []byte(bootstrap), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := fastconf.LoadBootstrap(context.Background(),
		fastconf.WithConfigPath(directory),
		fastconf.WithBootstrapPath(""),
	)
	if err != nil {
		t.Fatalf("LoadBootstrap: %v", err)
	}
	if loaded.Remote == nil {
		t.Fatal("remote configuration is missing")
	}
	if loaded.Remote.URL != "https://config.example.com/config" || loaded.Remote.Timeout != 3*time.Second {
		t.Fatalf("unexpected remote: %#v", loaded.Remote)
	}
	if loaded.Remote.Auth.Type != "bearer" || loaded.Remote.Auth.Token != "file-token" {
		t.Fatalf("unexpected auth: %#v", loaded.Remote.Auth)
	}

	// BOOTSTRAP_* variables override the bootstrap document.
	t.Setenv("BOOTSTRAP_REMOTE__TIMEOUT", "7s")
	overridden, err := fastconf.LoadBootstrap(context.Background(),
		fastconf.WithConfigPath(directory),
		fastconf.WithBootstrapPath(""),
	)
	if err != nil {
		t.Fatalf("LoadBootstrap with env: %v", err)
	}
	if overridden.Remote.Timeout != 7*time.Second {
		t.Fatalf("timeout = %v", overridden.Remote.Timeout)
	}
}
