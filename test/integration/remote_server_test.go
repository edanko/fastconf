//go:build integration

// Package integration contains end-to-end tests that run real binaries.
//
// Run them with:
//
//	go test -tags=integration -count=1 -v ./test/integration
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/edanko/fastconf"
)

// TestRemoteLayerIntegration starts fastconf-server, points a client loader at
// its /v1/config endpoint through a bootstrap document, and verifies that the
// remote layer loads values, tenants and auth, then picks up a reload.
func TestRemoteLayerIntegration(t *testing.T) {
	binary := buildServer(t)

	servedDirectory := t.TempDir()
	servedConfig := filepath.Join(servedDirectory, "config.yaml")
	writeFile(t, servedConfig, `config:
  domain: served.example
  service:
    url: https://served.example
  tenants:
    acme:
      domain: acme.served.example
`)

	server := startServer(t, binary, servedConfig, "integration-token")
	t.Cleanup(func() { server.stop(t) })

	clientDirectory := t.TempDir()
	writeFile(t, filepath.Join(clientDirectory, "config.yaml"), `config:
  domain: local.example
  localOnly: local-value
`)
	writeFile(t, filepath.Join(clientDirectory, "bootstrap.yml"), fmt.Sprintf(`config:
  remote:
    url: http://%s/v1/config
    timeout: 5s
    auth:
      type: bearer
      token: integration-token
`, server.address))

	loader, err := fastconf.New(
		fastconf.WithConfigPath(clientDirectory),
		fastconf.WithBootstrapPath(""),
		fastconf.WithoutEnvironmentVariables(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	ctx := context.Background()
	var config struct {
		Domain    string `mapstructure:"domain"`
		LocalOnly string `mapstructure:"localOnly"`
		Service   struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"service"`
	}
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if config.Domain != "served.example" {
		t.Fatalf("domain = %q, want remote value to override the local file", config.Domain)
	}
	if config.LocalOnly != "local-value" {
		t.Fatalf("localOnly = %q, want local values to survive the remote merge", config.LocalOnly)
	}
	if config.Service.URL != "https://served.example" {
		t.Fatalf("service.url = %q", config.Service.URL)
	}

	var tenant struct {
		TenantCode string `mapstructure:"tenantCode"`
		Domain     string `mapstructure:"domain"`
	}
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant: %v", err)
	}
	if tenant.TenantCode != "acme" || tenant.Domain != "acme.served.example" {
		t.Fatalf("tenant = %#v, want the remote tenant with its code injected", tenant)
	}

	// A client with a wrong token must fail to load, proving the remote layer
	// enforces the bearer authenticator end to end.
	wrongDirectory := t.TempDir()
	writeFile(t, filepath.Join(wrongDirectory, "bootstrap.yml"), fmt.Sprintf(`config:
  remote:
    url: http://%s/v1/config
    timeout: 5s
    auth:
      type: bearer
      token: wrong-token
`, server.address))
	wrongLoader, err := fastconf.New(
		fastconf.WithConfigPath(wrongDirectory),
		fastconf.WithBootstrapPath(""),
		fastconf.WithoutEnvironmentVariables(),
	)
	if err != nil {
		t.Fatalf("New wrong token loader: %v", err)
	}
	t.Cleanup(func() { _ = wrongLoader.Close() })
	if err := wrongLoader.Decode(ctx, &struct{}{}); err == nil {
		t.Fatal("Decode with a wrong remote token succeeded, want an error")
	}

	// Reload the served configuration, reload the server through its API,
	// then reload the client and verify the remote layer fetches fresh values.
	writeFile(t, servedConfig, `config:
  domain: reloaded.example
  service:
    url: https://reloaded.example
  tenants:
    acme:
      domain: acme.reloaded.example
`)
	postReload(t, server.address, "integration-token")

	if err := loader.Reload(ctx); err != nil {
		t.Fatalf("client Reload: %v", err)
	}
	config = struct {
		Domain    string `mapstructure:"domain"`
		LocalOnly string `mapstructure:"localOnly"`
		Service   struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"service"`
	}{}
	if err := loader.Decode(ctx, &config); err != nil {
		t.Fatalf("Decode after reload: %v", err)
	}
	if config.Domain != "reloaded.example" || config.Service.URL != "https://reloaded.example" {
		t.Fatalf("configuration after reload = %#v", config)
	}
	tenant = struct {
		TenantCode string `mapstructure:"tenantCode"`
		Domain     string `mapstructure:"domain"`
	}{}
	if err := loader.DecodeTenant(ctx, "acme", &tenant); err != nil {
		t.Fatalf("DecodeTenant after reload: %v", err)
	}
	if tenant.Domain != "acme.reloaded.example" {
		t.Fatalf("tenant after reload = %#v", tenant)
	}
}

// buildServer compiles the fastconf-server command into a temporary directory.
func buildServer(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fastconf-server")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/fastconf-server")
	command.Dir = moduleRoot(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fastconf-server: %v\n%s", err, output)
	}
	return binary
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the integration test source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

type serverProcess struct {
	address string
	process *exec.Cmd
	logs    *lockedBuffer
}

// startServer starts fastconf-server on an ephemeral port and waits for the
// listener address from its log output.
func startServer(t *testing.T, binary, configPath, token string) *serverProcess {
	t.Helper()
	command := exec.Command(binary,
		"-config_path", configPath,
		"-listen", "127.0.0.1:0",
		"-token", token,
	)
	command.Env = serverEnvironment()
	command.Stdout = io.Discard
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start fastconf-server: %v", err)
	}

	logs := &lockedBuffer{}
	addresses := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			logs.WriteString(line + "\n")
			if address, ok := parseListenAddress(line); ok {
				select {
				case addresses <- address:
				default:
				}
			}
		}
	}()

	select {
	case address := <-addresses:
		return &serverProcess{address: address, process: command, logs: logs}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("fastconf-server did not report a listen address; logs:\n%s", logs.String())
		return nil
	}
}

func (s *serverProcess) stop(t *testing.T) {
	t.Helper()
	if s.process == nil || s.process.Process == nil {
		return
	}
	_ = s.process.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.process.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = s.process.Process.Kill()
		<-done
		t.Errorf("fastconf-server did not shut down after SIGTERM; logs:\n%s", s.logs.String())
	}
}

// parseListenAddress extracts the address field from the structured start log.
func parseListenAddress(line string) (string, bool) {
	for field := range strings.FieldsSeq(line) {
		if address, ok := strings.CutPrefix(field, "address="); ok {
			return address, true
		}
	}
	return "", false
}

// serverEnvironment removes CONFIG_* variables so the served configuration is
// independent of the CI environment.
func serverEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "CONFIG") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

func postReload(t *testing.T, address, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/v1/reload", nil)
	if err != nil {
		t.Fatalf("create reload request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("reload request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reload status = %d: %s", response.StatusCode, body)
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode reload response: %v", err)
	}
	if result["status"] != "reloaded" {
		t.Fatalf("reload response = %s", body)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type lockedBuffer struct {
	mutex sync.Mutex
	data  strings.Builder
}

func (b *lockedBuffer) WriteString(value string) {
	b.mutex.Lock()
	b.data.WriteString(value)
	b.mutex.Unlock()
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.data.String()
}
