package fastconf

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edanko/fastconf/auth"
	"github.com/edanko/fastconf/internal/decode"
	"github.com/edanko/fastconf/internal/hierarchy"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
	"github.com/edanko/fastconf/internal/singleflight"
	"github.com/edanko/fastconf/layers"
	"github.com/edanko/fastconf/retry"
)

// Loader loads, merges and decodes configuration.
//
// Loader is safe for concurrent use. The first call that needs configuration
// loads it once and publishes an immutable snapshot; subsequent calls read the
// snapshot without locking. Reload replaces the snapshot atomically.
type Loader struct {
	options     options
	fileSources []layers.Layer
	envSource   layers.Layer
	decoder     *decode.Decoder
	logger      *slog.Logger

	snapshot   atomic.Pointer[snapshot]
	loadMutex  sync.Mutex
	flights    singleflight.Group
	remoteMu   sync.Mutex
	remote     layers.Layer
	remoteDone bool
	closed     atomic.Bool
}

// New creates a configuration loader.
func New(optionList ...Option) (*Loader, error) {
	opts := defaultOptions()
	for _, option := range optionList {
		if option != nil {
			option(&opts)
		}
	}
	return newLoader(opts)
}

func newLoader(opts options) (*Loader, error) {
	if opts.application != "" && opts.subsystem == "" {
		return nil, fmt.Errorf("fastconf: application %q requires a subsystem", opts.application)
	}
	loader := &Loader{
		options: opts,
		logger:  opts.logger,
		decoder: decode.New(decode.Config{
			WeaklyTyped: opts.decoderConfig.WeaklyTypedInput,
			Strict:      opts.decoderConfig.Strict,
			TagName:     opts.decoderConfig.TagName,
			Hooks:       opts.decoderConfig.DecodeHooks,
		}),
	}
	if opts.configPath != "" {
		sources, err := buildFileLayers(opts.configPath, opts.subsystem, opts.application, opts.environment)
		if err != nil {
			return nil, err
		}
		loader.fileSources = sources
	}
	if opts.envEnabled {
		loader.envSource = layers.NewEnv(opts.envPrefix, layers.WithEnvSeparator(opts.envSeparator))
	}
	return loader, nil
}

// Decode loads configuration and decodes the configuration root into the
// destination, which must be a non-nil pointer.
func (l *Loader) Decode(ctx context.Context, destination any) error {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return err
	}
	current.ensureTree()
	if err := l.decoder.Decode(&current.rootValue, destination); err != nil {
		return err
	}
	return l.validate(destination)
}

// DecodeTenant loads configuration and decodes one tenant configuration into
// the destination, which must be a non-nil pointer.
func (l *Loader) DecodeTenant(ctx context.Context, tenantCode string, destination any) error {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return err
	}
	tenant, ok := current.tenant(tenantCode)
	if !ok {
		return fmt.Errorf("%w: %q", ErrTenantNotFound, tenantCode)
	}
	tenantValue := valueOfObject(tenant)
	if err := l.decoder.Decode(&tenantValue, destination); err != nil {
		return err
	}
	return l.validate(destination)
}

// Map returns a deep copy of the configuration root.
func (l *Loader) Map(ctx context.Context) (map[string]any, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return maps.DeepCopyMap(current.config), nil
}

// TenantMap returns a deep copy of one tenant configuration.
func (l *Loader) TenantMap(ctx context.Context, tenantCode string) (map[string]any, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	tenant, ok := current.tenantMap(tenantCode)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTenantNotFound, tenantCode)
	}
	return maps.DeepCopyMap(tenant), nil
}

// Document returns a deep copy of the whole normalized document, including the
// configuration root wrapper.
func (l *Loader) Document(ctx context.Context) (map[string]any, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return maps.DeepCopyMap(current.document), nil
}

// TenantCodes returns the sorted tenant codes present in the configuration.
func (l *Loader) TenantCodes(ctx context.Context) ([]string, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), current.tenantCodes...), nil
}

// Get returns the configuration value at a dot-separated path. Path segments
// are matched case-insensitively.
func (l *Loader) Get(ctx context.Context, path string) (any, bool, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	value, ok := maps.GetFold(current.config, splitPath(path))
	if !ok {
		return nil, false, nil
	}
	return maps.DeepCopy(value), true, nil
}

// Reload reloads configuration and atomically replaces the snapshot.
//
// Concurrent reloads are coalesced: one caller performs the load and every
// concurrent caller receives the same result. A failed reload keeps the
// previous snapshot in place.
func (l *Loader) Reload(ctx context.Context) error {
	if l.closed.Load() {
		return ErrClosed
	}
	_, err := singleflight.DoValue(&l.flights, "reload", func() (struct{}, error) {
		return struct{}{}, l.reload(ctx)
	})
	return err
}

// Refresh reloads configuration. It makes Loader satisfy refresh interfaces.
func (l *Loader) Refresh(ctx context.Context) error {
	return l.Reload(ctx)
}

// Close releases resources held by remote sources.
func (l *Loader) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	if closer, ok := l.remote.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (l *Loader) currentSnapshot(ctx context.Context) (*snapshot, error) {
	if current := l.snapshot.Load(); current != nil {
		return current, nil
	}
	if l.closed.Load() {
		return nil, ErrClosed
	}
	current, err := singleflight.DoValue(&l.flights, "initial-load", func() (*snapshot, error) {
		return l.loadSnapshot(ctx)
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

// loadSnapshot performs the initial load. The singleflight group guarantees a
// single execution, while the snapshot check repeats under the mutex to
// cooperate with a reload that may have completed first.
func (l *Loader) loadSnapshot(ctx context.Context) (*snapshot, error) {
	l.loadMutex.Lock()
	defer l.loadMutex.Unlock()
	if current := l.snapshot.Load(); current != nil {
		return current, nil
	}
	document, err := l.loadDocument(ctx)
	if err != nil {
		return nil, err
	}
	current := newSnapshot(document)
	l.snapshot.Store(current)
	return current, nil
}

// reload loads a fresh document and publishes it.
func (l *Loader) reload(ctx context.Context) error {
	l.loadMutex.Lock()
	defer l.loadMutex.Unlock()
	if l.closed.Load() {
		return ErrClosed
	}

	started := time.Now()
	document, err := l.loadDocument(ctx)
	if err != nil {
		if l.logger != nil {
			l.logger.WarnContext(ctx, "fastconf: reload failed, keeping previous configuration", "error", err)
		}
		return err
	}
	l.snapshot.Store(newSnapshot(document))
	if l.logger != nil {
		l.logger.DebugContext(ctx, "fastconf: configuration reloaded", "duration", time.Since(started))
	}
	return nil
}

func (l *Loader) loadDocument(ctx context.Context) (map[string]any, error) {
	values := hierarchy.Initial(l.options.defaultTenant)
	var err error
	for _, source := range l.fileSources {
		values, err = source.Load(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	for _, source := range l.options.sources {
		values, err = source.Load(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	remote, err := l.remoteSource(ctx)
	if err != nil {
		return nil, err
	}
	if remote != nil {
		values, err = remote.Load(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	if l.envSource != nil {
		values, err = l.envSource.Load(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	if err := hierarchy.Collapse(values, hierarchy.Params{
		Subsystem:   l.options.subsystem,
		Application: l.options.application,
		Environment: l.options.environment,
	}); err != nil {
		return nil, err
	}
	if err := hierarchy.MergeRootIntoTenants(values); err != nil {
		return nil, err
	}
	if err := hierarchy.SetTenantCodes(values); err != nil {
		return nil, err
	}
	if l.envSource != nil {
		values, err = l.envSource.Load(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	if err := hierarchy.Resolve(ctx, values); err != nil {
		return nil, err
	}
	return values, nil
}

func (l *Loader) remoteSource(ctx context.Context) (layers.Layer, error) {
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	if l.remoteDone {
		return l.remote, nil
	}
	source, err := l.buildRemoteSource(ctx)
	if err != nil {
		return nil, err
	}
	l.remote = source
	l.remoteDone = true
	return source, nil
}

func (l *Loader) buildRemoteSource(ctx context.Context) (layers.Layer, error) {
	config := l.options.remote
	if config == nil && l.options.bootstrap != nil {
		config = l.options.bootstrap.Remote
	}
	if config == nil && l.options.bootstrapEnabled {
		bootstrap, err := l.loadBootstrap(ctx)
		if err != nil {
			return nil, err
		}
		config = bootstrap.Remote
	}
	if config == nil {
		return nil, nil
	}
	if config.URL == "" {
		return nil, ErrNoRemoteURL
	}
	if _, err := url.ParseRequestURI(config.URL); err != nil {
		return nil, fmt.Errorf("fastconf: invalid remote URL %q: %w", config.URL, err)
	}
	authenticator, err := auth.FromConfig(config.Auth)
	if err != nil {
		return nil, fmt.Errorf("fastconf: remote authentication: %w", err)
	}
	options := []layers.HTTPOption{
		layers.WithHTTPHeaders(config.Headers),
		layers.WithHTTPCacheDir(config.CacheDir),
		layers.WithHTTPStaleOnError(config.UseStale),
		layers.WithHTTPTimeout(config.Timeout),
		layers.WithHTTPRetry(l.remoteRetryPolicy(config)),
	}
	if authenticator != nil {
		options = append(options, layers.WithHTTPAuth(authenticator))
	}
	if l.options.httpClient != nil {
		options = append(options, layers.WithHTTPClient(l.options.httpClient))
	} else {
		options = append(options, layers.WithHTTPClient(&http.Client{}))
	}
	return layers.NewHTTP(config.URL, options...), nil
}

func (l *Loader) remoteRetryPolicy(config *RemoteConfig) retry.Policy {
	if l.options.retryPolicy != nil {
		return *l.options.retryPolicy
	}
	if config.Retry != nil {
		return *config.Retry
	}
	policy := retry.Default()
	if config.FailFast {
		policy.MaxAttempts = 1
	}
	return policy
}

func (l *Loader) loadBootstrap(ctx context.Context) (*Bootstrap, error) {
	return loadBootstrapDocument(ctx, l.options)
}

// loadBootstrapDocument loads the bootstrap document described by opts.
//
// The bootstrap document is loaded with the same file hierarchy as the
// application, with environment overrides read through the BOOTSTRAP prefix,
// and with the bootstrap file itself as the highest priority source.
func loadBootstrapDocument(ctx context.Context, opts options) (*Bootstrap, error) {
	opts.bootstrapEnabled = false
	opts.bootstrap = nil
	opts.remote = nil
	opts.envEnabled = true
	opts.envPrefix = "BOOTSTRAP"

	path := bootstrapFilePath(opts.configPath, opts.bootstrapPath)
	if _, err := os.Stat(path); err != nil {
		if opts.bootstrapPath != "" {
			return nil, fmt.Errorf("fastconf: stat bootstrap path %q: %w", path, err)
		}
		return &Bootstrap{}, nil
	}
	opts.sources = append(append([]layers.Layer(nil), opts.sources...), layers.NewFile(path))

	bootstrapLoader, err := newLoader(opts)
	if err != nil {
		return nil, err
	}
	document, err := bootstrapLoader.loadDocument(ctx)
	if err != nil {
		return nil, fmt.Errorf("fastconf: load bootstrap: %w", err)
	}
	config, ok := maps.GetMap(document, []string{sections.Root})
	if !ok {
		return &Bootstrap{}, nil
	}
	bootstrap := &Bootstrap{}
	if err := bootstrapLoader.decoder.Decode(valueOfMap(config), bootstrap); err != nil {
		return nil, fmt.Errorf("fastconf: decode bootstrap: %w", err)
	}
	return bootstrap, nil
}

func (l *Loader) validate(destination any) error {
	if err := validateStruct(destination); err != nil {
		return fmt.Errorf("fastconf: validate configuration: %w", err)
	}
	if l.options.validator != nil {
		if err := l.options.validator(destination); err != nil {
			return fmt.Errorf("fastconf: validate configuration: %w", err)
		}
	}
	return nil
}
