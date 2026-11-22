# fastconf

Layered, hierarchical configuration for Go microservices with a fast decode
path.

`fastconf` reads configuration from files, directories, environment variables,
HTTP endpoints and custom sources; merges it through a subsystem/application/
environment/tenant hierarchy; resolves `${placeholder}` expressions; and
decodes the result into Go structs.

## Features

- **Layered sources** — files (YAML, JSON, TOML), directories, environment
  variables, in-memory documents, HTTP endpoints and custom `Layer`
  implementations.
- **Hierarchical model** — product, subsystem, application, environment and
  tenant scopes with deterministic precedence.
- **Complete tenant configurations** — every tenant receives the merged root
  configuration with tenant-specific overrides and an injected `tenantCode`.
- **Placeholders** — typed `${path}` lookups, defaults, `${path:default}`,
  `#{ condition ? then : else }` expressions and `*${list}` unpacking.
- **Struct decoding** — weak typing, `mapstructure` tags, decode hooks, embed
  flattening, strict mode, self-validation and pluggable validators.
- **Remote configuration** — retries with exponential backoff, bearer/basic/
  OAuth2 client-credentials/HMAC authentication, conditional requests, stale
  disk cache fallback and asynchronous debounced refresh.
- **Consistent zero-copy views** — `Loader.View` pins one immutable snapshot;
  typed reads are allocation-free and struct decoding does not copy data.
- **Coalesced loading** — concurrent decodes and reloads share one in-flight
  load through an internal singleflight group.
- **Low allocation decode** — immutable value trees, cached reflection plans and
  zero map allocations per decoded struct.
- **Extensible encodings** — register custom JSON/YAML/TOML decoders with
  `encoding.Register`; registered decoders are normalized automatically.
- **encoding/json/v2 when available** — JSON sources use the standard v2
  decoder on toolchains that enable it (the 1.27 target), with an automatic v1
  fallback and no dependency.

## Installation

```bash
go get github.com/edanko/fastconf
```

## Quick start

```go
package main

import (
	"context"
	"log"

	"github.com/edanko/fastconf"
)

type Config struct {
	Domain   string                `mapstructure:"domain"`
	Database DatabaseConfig        `mapstructure:"database"`
	Tenants  map[string]TenantConfig `mapstructure:"tenants"`
}

type DatabaseConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type TenantConfig struct {
	TenantCode string `mapstructure:"tenantCode"`
	Domain     string `mapstructure:"domain"`
}

func main() {
	loader, err := fastconf.New(
		fastconf.WithConfigPath("/etc/billing"),
		fastconf.WithSubsystem("payments"),
		fastconf.WithApplication("billing"),
		fastconf.WithEnvironment("production"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer loader.Close()

	var config Config
	if err := loader.Decode(context.Background(), &config); err != nil {
		log.Fatal(err)
	}
	_ = config
}
```

## Configuration files

A document may wrap its values in a `config` object. Bare documents are wrapped
automatically, so both files below are equivalent:

```yaml
config:
  database:
    host: localhost
    port: 5432
```

```yaml
database:
  host: localhost
  port: 5432
```

When `WithConfigPath` points to a directory, files are merged in this order:

```
*.yaml, *.yml, *.json, *.toml
subsystems/<subsystem>/
subsystems/<subsystem>/applications/<application>/
tenants/<tenant>/
tenants/<tenant>/subsystems/<subsystem>/
environments/<environment>/
environments/<environment>/subsystems/<subsystem>/
environments/<environment>/subsystems/<subsystem>/applications/<application>/
environments/<environment>/tenants/<tenant>/
environments/<environment>/tenants/<tenant>/subsystems/<subsystem>/
```

Values from a directory file are placed into the matching section:

- files under `subsystems/<subsystem>/` become `config.subsystems.<subsystem>`;
- files under `subsystems/<subsystem>/applications/<application>/` become
  `config.subsystems.<subsystem>.applications.<application>`;
- files under `tenants/<tenant>/` become `config.tenants.<tenant>`;
- files under `environments/<environment>/` become
  `config.environments.<environment>`;
- and so on.

At load time the active subsystem, application and environment are collapsed
into the root configuration, then into every tenant. The `subsystems`,
`applications` and `environments` sections are removed from the final document.

## Environment variables

Environment variable overrides are enabled by default with the prefix
`CONFIG` and the path separator `__`:

```bash
export CONFIG_DATABASE__HOST=env-db
export CONFIG_DATABASE__PORT=6432
```

```go
loader, _ := fastconf.New(
	fastconf.WithConfigPath("config.yaml"),
	fastconf.WithEnvPrefix("MYAPP"),
	fastconf.WithEnvSeparator("__"),
)
```

Use `fastconf.WithoutEnvironmentVariables()` to disable the source and
`fastconf.WithEnvPrefix("")` to read unprefixed variables.

## Placeholders

```yaml
config:
  domain: example.com
  tls: true
  baseURL: '#{ ${tls} ? "https" : "http" }://${domain}'
  adminURL: ${baseURL}/admin
  database:
    host: ${DB_HOST:localhost}
  allowedDomains:
    - default.example
    - "*${domains}"
  domains:
    - example.com
    - example.org
```

- `${path}` performs a case-insensitive lookup. If the path is not found, the
  environment is consulted.
- `${path:default}` supplies a typed default (`true`, `42`, `"text"`, `5s`,
  `["a", "b"]`).
- `#{ ... }` supports `!`, `and`, `or`, parentheses, `==`, `!=`, `>`, `>=`,
  `<`, `<=` and `condition ? then : else`. Branches may be literals, variables,
  nested conditionals or quoted templates.
- `*${list}` unpacks a list into its parent list.

Resolution errors are reported with the configuration path, and cycles are
detected.

## Tenants

```go
var tenant TenantConfig
err := loader.DecodeTenant(ctx, "acme", &tenant)

codes, err := loader.TenantCodes(ctx)
tenantMap, err := loader.TenantMap(ctx, "acme")
```

A default tenant named `default` always exists. Change it with
`WithDefaultTenant("acme")` or disable it with `WithoutDefaultTenant()`.
Tenant lookup is case-insensitive.

## Consistent read views

`Loader.View` pins the current immutable snapshot for zero-copy reads:

```go
view, err := loader.View(ctx)
if err != nil {
	return err
}

host, ok := view.String("database.host") // scalar reads, no allocation
tenantView, ok := view.Tenant("acme")
if ok {
	var tenant TenantConfig
	err = tenantView.Decode(&tenant) // zero-copy struct decode
}
```

A view keeps observing the snapshot it was created from: call `loader.View`
again after `Reload` to see fresh values. Scalar reads and `Decode` do not copy
configuration data, `Get` returns deep copies for objects and lists so callers
may modify them safely, and `Map`/`TenantMap` remain available when a mutable
map of the whole scope is required.

| Read | time/op | allocations |
| --- | ---: | ---: |
| `view.String("domain")` | 11.7 ns | 0 |
| `tenantView.Decode(&cfg)` | 142 ns | 1 |
| `loader.Get(ctx, "tenants.tenant1.domain")` | 46 ns | 1 |

## Remote configuration

```yaml
config:
  remote:
    url: https://config.example.com/v1/config
    timeout: 5s
    headers:
      X-Environment: production
    auth:
      type: oauth2
      tokenUrl: https://auth.example.com/token
      clientId: billing
      clientSecret: ${CONFIG_SECRET}
      scopes: [config.read]
    retry:
      maxAttempts: 6
      initialInterval: 1s
      maxInterval: 10s
      multiplier: 2
    cacheDir: /var/cache/billing
    useStale: true
```

Load the bootstrap document with `fastconf.WithBootstrapPath("")` (an empty
path means `bootstrap.yml` next to the configuration path) or configure the
remote source directly with `fastconf.WithRemote(...)`.

```go
loader, err := fastconf.New(
	fastconf.WithConfigPath("/etc/billing"),
	fastconf.WithBootstrapPath(""),
	fastconf.WithRetry(retry.Default()),
)
```

Authentication options:

- `auth.Bearer(token)`, `auth.Basic(username, password)`,
  `auth.Header(name, value)`, `auth.Headers(map)`;
- `auth.OAuth2ClientCredentials(auth.OAuth2Config{...})` with token caching and
  refresh;
- `auth.HMAC(secret)` with timestamped SHA-256 request signatures.

## Asynchronous refresh

```go
refresher := refresher.Start(ctx, loader, refresher.WithTimeout(30*time.Second))

// Call on a configuration update notification.
refresher.Refresh(ctx, nil, func(err error) {
	if err != nil {
		log.Printf("refresh: %v", err)
	}
})
```

`Loader.Reload(ctx)` performs a synchronous reload and atomically replaces the
snapshot.

## Configuration API server

A standalone server is included in [`cmd/fastconf-server`](cmd/fastconf-server/main.go):

```bash
go build -o bin/fastconf-server ./cmd/fastconf-server

./bin/fastconf-server \
  -config_path /etc/billing \
  -subsystem payments \
  -environment production \
  -listen 127.0.0.1:8080
```

The server loads configuration before binding its listener, so a broken
configuration fails fast. Endpoints:

| Method | Path | Description |
| --- | --- | --- |
| GET | `/healthz` | health probe |
| GET | `/v1/config` | configuration root as JSON |
| GET | `/v1/config/database/host` | value lookup by slash-separated path |
| GET | `/v1/tenants` | tenant codes |
| GET | `/v1/tenants/acme` | one tenant configuration |
| POST | `/v1/reload` | reload configuration |

```bash
curl -s http://127.0.0.1:8080/v1/config/database/host
# {"path":"database.host","value":"localhost"}
```

Send `SIGHUP` to reload configuration without dropping connections. Request
logging goes to standard error.

The server binds to loopback by default because configuration may contain
secrets. It listens on all interfaces only when `-listen` asks for it. Set
`-token` or `FASTCONF_SERVER_TOKEN` to require
`Authorization: Bearer <token>` on every request.

## Custom sources

```go
type secretsLayer struct {
	client *secrets.Client
}

func (l secretsLayer) Load(ctx context.Context, values map[string]any) (map[string]any, error) {
	document, err := l.client.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	return layers.NewInMemory(document).Load(ctx, values)
}

loader, _ := fastconf.New(
	fastconf.WithConfigPath("config.yaml"),
	fastconf.WithLayer(secretsLayer{client: client}),
)
```

Custom sources run after file sources and before environment overrides.

## Validation

```go
type Config struct {
	Port int `mapstructure:"port"`
}

func (c *Config) Validate() error {
	if c.Port <= 0 {
		return errors.New("port must be positive")
	}
	return nil
}
```

Self-validation runs after decoding. A pluggable validator is available through
`fastconf.WithValidator(func(any) error)`; it composes with
[go-playground/validator](https://github.com/go-playground/validator) or any
other validation library.

## Strict decoding

```go
loader, _ := fastconf.New(
	fastconf.WithConfigPath("config.yaml"),
	fastconf.WithStrictDecoding(),
)
```

Unknown configuration keys then fail decoding with the configuration path.

## Testing

```bash
# Unit and package tests.
go test ./...

# Race detector.
go test -race ./...

# End-to-end: builds cmd/fastconf-server, serves configuration over HTTP,
# loads it through the bootstrap remote layer, checks bearer auth and reload.
go test -tags=integration -count=1 -v ./test/integration
```

CI runs the unit and race suites, a dedicated server/remote-layer integration
job, the linter, govulncheck and a benchmark smoke test.

## Performance

The decode path converts a document once into an immutable value tree and uses
cached reflection plans per type. Decoding does not allocate maps or re-parse
struct tags.

Representative results on an AMD Ryzen 7 9700X (Go 1.27, `-benchmem`):

| Benchmark | time/op | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| `DecodeStruct` (root + 1001 tenants) | 173 µs | 213 KB | 9 |
| `DecodeTenantStruct` | 159 ns | 80 B | 1 |
| `ViewGetScalar` | 11.7 ns | 0 B | 0 |
| `ViewDecodeTenant` | 142 ns | 80 B | 1 |
| `DecodeTenantMap` | 208 ns | 672 B | 4 |
| `DecodeMap` | 237 µs | 755 KB | 4,010 |
| `DecodeSmallStruct` | 91 ns | 32 B | 1 |
| `LoadDocument` (full pipeline, 1001 tenants) | 0.73 ms | 1.28 MB | 8,045 |

The 1001-tenant struct decode allocates nine objects in total: the destination
map, its bucket array, one temporary for map entries and the destination
escape required by `reflect`. Per-tenant and per-field decoding is
allocation-free. See [docs/BENCHMARKS.md](docs/BENCHMARKS.md) for the
measurement methodology.

Run the benchmarks yourself:

```bash
go test -run '^$' -bench . -benchmem -count=10 ./... | tee bench.txt
benchstat bench.txt
```

## License

MIT. See [LICENSE](LICENSE).
