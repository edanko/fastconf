# fastconf design

`fastconf` is a configuration library for Go microservices. It loads
configuration from layered sources, applies a hierarchical merge model
(subsystems, applications, environments, tenants), resolves placeholder
expressions, and decodes the result into Go structs with low allocation cost.

## Goals

- Feature-complete layered configuration: files, directories, environment
  variables, in-memory documents, HTTP sources, custom layers.
- Hierarchical merge model with tenants, subsystems, applications and
  environments.
- Placeholder expressions with typed evaluation and conditionals.
- Struct decoding with weak typing, decode hooks and validation.
- Remote configuration with retries, authentication, optional stale cache and
  asynchronous refresh.
- Predictable performance: the decode path must not allocate per field and must
  not use reflection on every call beyond cached type plans.

## Non-goals

- Replacing `encoding/json` for arbitrary data binding.
- Storing secrets in plain text logs (secret wrappers are provided, logging is
  the caller's responsibility).

## Packages

| Package | Purpose |
| --- | --- |
| `fastconf` | Public `Loader`, options, bootstrap, validation, flags |
| `fastconf/layers` | Configuration sources (`Layer` implementations) |
| `fastconf/encoding` | YAML, JSON, TOML document decoding |
| `fastconf/hook` | Decode hooks |
| `fastconf/retry` | Retry policy with exponential backoff |
| `fastconf/auth` | HTTP authenticators (bearer, basic, OAuth2, HMAC) |
| `fastconf/refresher` | Asynchronous debounced refresh |
| `internal/configapi` | HTTP handler for the configuration API |
| `cmd/fastconf-server` | Standalone configuration API server |
| `test/integration` | End-to-end server + remote-layer tests |
| `internal/value` | Immutable value tree for fast decoding |
| `internal/maps` | Nested `map[string]any` helpers |
| `internal/doc` | Document normalization (`config` root) |
| `internal/sections` | Reserved section names |
| `internal/expr` | Placeholder expression compiler and resolver |
| `internal/decode` | Reflection-plan struct decoder |

## Data model

A configuration document is a `map[string]any` tree of objects, lists and
scalars. Every source produces such a document.

The canonical document wraps the configuration root in a `config` object:

```yaml
config:
  database:
    host: localhost
```

A bare document without a `config` key is wrapped automatically.

Reserved sections inside the configuration root:

- `subsystems.<subsystem>` — values for a subsystem, optionally with
  `applications.<application>`.
- `environments.<environment>` — values for an environment, optionally with
  `subsystems` and `tenants`.
- `tenants.<code>` — values for a tenant, optionally with `subsystems`.
- `tenantCode` — injected into every tenant with its code.

### Load pipeline

1. Start with an empty root document and the default tenant.
2. Execute source layers in order. Later sources override earlier ones.
3. Apply environment variables to the root.
4. Collapse the active subsystem and application into the root.
5. Collapse subsystem/application values into each tenant.
6. Collapse the active environment into the root, into each tenant, and merge
   environment specific tenant values.
7. Merge the root into every tenant so each tenant is a complete configuration.
8. Inject `tenantCode` into every tenant.
9. Apply environment variables again to the root.
10. Resolve placeholders in the root, then in every tenant.
11. Publish an immutable snapshot under an atomic pointer.
12. Coalesce concurrent work: the first load and concurrent reloads run once
    and every waiting caller receives the same result. Failures are not cached,
    so the next call retries.

## Public API sketch

```go
loader, err := fastconf.New(
    fastconf.WithConfigPath("/etc/myapp"),
    fastconf.WithApplication("billing"),
    fastconf.WithSubsystem("payments"),
    fastconf.WithEnvironment("production"),
)
var cfg Config
err = loader.Decode(ctx, &cfg)
err = loader.DecodeTenant(ctx, "tenant1", &tenantCfg)
m, err := loader.Map(ctx)

err = loader.SetOverride("features.newBilling", true) // runtime override
err = loader.SetTenantOverride("tenant1", "database.host", "db.internal")

view, err := loader.View(ctx)            // pinned immutable snapshot
host, ok := view.String("database.host") // allocation-free
err = view.WriteJSON(writer)             // streams the snapshot without copying
tenantView, ok := view.Tenant("tenant1")
err = tenantView.Decode(&tenantCfg)
```

## Layer contract

```go
type Layer interface {
    Load(ctx context.Context, values map[string]any) (map[string]any, error)
}
```

A layer merges its values into `values` and returns the resulting document.
Sources must not retain the document after returning. `doc.Normalize` must be
called before merging so the `config` root always exists.

## Package contracts

### encoding

```go
type Encoding string
type Decoder func(io.Reader) (map[string]any, error)
func Decode(reader io.Reader, enc Encoding) (map[string]any, error)
func FromFilename(name string) (Encoding, error)
func Register(enc Encoding, decoder Decoder)
func RegisterExtension(extension string, enc Encoding)
```

YAML, JSON and TOML are supported. Decoded documents must be normalized:
`map[string]any` objects, `[]any` lists, scalars unchanged.

### layers (sources)

```go
func NewFile(path string, opts ...FileOption) *File
func NewGlob(fsys fs.FS, patterns ...string) *Glob
func NewDir(path string, opts ...DirOption) *Dir
func NewReader(reader io.ReadSeeker, enc encoding.Encoding, opts ...ReaderOption) *Reader
func NewEnv(prefix string, opts ...EnvOption) *Env
func NewInMemory(document map[string]any) *InMemory
func NewHTTP(url string, opts ...HTTPOption) *HTTP
func NewChain(layers ...Layer) *Chain
func NewNoop() Noop
func NewOnce(inner Layer) *Once
```

`NewFile` accepts one file. `NewGlob` merges every file matching the patterns
in deterministic sorted order. `NewDir` loads supported files under a directory
recursively in sorted order. All three normalize documents and merge them into
the incoming document.

### internal/expr

```go
func Resolve(ctx context.Context, values map[string]any) error
func Compile(source string) (*Template, error)
func (t *Template) Eval(scope Scope) (any, error)
type Scope interface { Lookup(path string) (any, bool) }
```

Syntax:

- `${path}` — value lookup, case-insensitive path segments, environment
  fallback, recursive resolution of referenced strings, cycle detection.
- `${path:default}` — default value when the path or environment variable is
  missing. Defaults are typed: `true`, `false`, numbers, quoted strings, bare
  strings, arrays.
- `#{ expression }` — conditional expression with `!`, `and`, `or`, parentheses,
  comparisons (`==`, `!=`, `>`, `>=`, `<`, `<=`), `condition ? then : else`.
  Branches may be literals, variables, nested conditionals or quoted templates.
- `*${path}` — array unpack, only valid as the whole value or as a list element.
- Any other text is a literal fragment.
- String values containing multiple fragments are concatenated. A single
  expression fragment returns its typed value.

`Resolve` walks the document recursively, resolving every string value, and
mutates the map in place. Errors must include the configuration path.

### internal/decode

```go
type Config struct {
    WeaklyTyped bool
    Strict      bool
    TagName     string
    Hooks       []hook.Func
}
type Decoder struct{ /* ... */ }
func New(cfg Config) *Decoder
func (d *Decoder) Decode(src *value.Value, dst any) error
```

Rules:

- `dst` must be a non-nil pointer.
- Struct field names resolve from the tag (default `mapstructure`, then `json`,
  `yaml`, `toml`) or the Go field name, case-insensitively.
- Supported tag options: `squash`, `remain`, other options ignored.
- Embedded anonymous struct fields are flattened unless they have an explicit
  name tag.
- Weak conversions: string to bool/int/uint/float/duration/time/URL,
  int/float/bool to string, strings to slices by comma, scalars to slices.
- `time.Time` from RFC 3339 or `2006-01-02` strings and unix seconds.
- `time.Duration` from duration strings and integers.
- `url.URL` from strings.
- `encoding.TextUnmarshaler` from strings.
- Lists, arrays, maps (`map[string]T` and string-keyed maps), pointers and
  `any` are supported.
- Strict mode fails on unknown object keys.
- Type plans are cached per `reflect.Type` in a `sync.Map`; decoding an object
  must not allocate a map per struct.
- Error messages include the configuration path.

### hook

```go
type Func func(from, to reflect.Type, data any) (any, error)
func Compose(funcs ...Func) Func
func StringToTimeDuration() Func
func StringToTime(layouts ...string) Func
func StringToURL() Func
func StringToSlice(separator string) Func
var Default Func
```

### retry

```go
type Policy struct {
    MaxAttempts     int
    InitialInterval time.Duration
    MaxInterval     time.Duration
    Multiplier      float64
    Jitter          float64
}
func Default() Policy
func (p Policy) Do(ctx context.Context, fn func(context.Context) error) error
```

### auth

```go
type Authenticator interface { Apply(req *http.Request) error }
func Bearer(token string) Authenticator
func Basic(username, password string) Authenticator
func Header(name, value string) Authenticator
func Headers(headers map[string]string) Authenticator
func OAuth2ClientCredentials(cfg OAuth2Config) (Authenticator, error)
func HMAC(secret []byte, opts ...HMACOption) Authenticator
type Config struct { /* decodable static configuration */ }
func FromConfig(cfg Config) (Authenticator, error)
```

### layers (HTTP)

```go
func NewHTTP(rawURL string, opts ...HTTPOption) *HTTP
```

Fetch a document over HTTP, decode by `Content-Type`, URL extension or explicit
encoding, normalize, merge. Options: HTTP client, headers, authenticator,
timeout, retry policy, cache directory for stale-on-error fallback, conditional
requests using a modification time from the context.

### refresher

```go
type Refresher interface { Refresh(ctx context.Context) error }
func Start(ctx context.Context, refresher Refresher, opts ...Option) *Async
func (a *Async) Refresh(ctx context.Context, modifiedAt *time.Time, ack func(error))
```

Debounce concurrent requests and execute refreshes one at a time with a
timeout.

## Performance rules

- No `reflect.Value` allocation in the decode loop beyond cached plans.
- No per-field `map[string]any` construction while decoding.
- Scalars are stored unboxed in `internal/value`.
- Hot paths must be benchmarked with `b.Loop()`, `-benchmem`, `-count=10` and
  compared with `benchstat`.
- Load-time work (parsing, merging, resolution) is separate from decode-time
  work and benchmarked separately.

## Clean-room rule

All code is original work. Do not copy source, comments, identifiers or
copyright headers from any other configuration library, and do not reference
other projects in code, tests, comments, docs or commit messages.
