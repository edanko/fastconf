// Package fastconf loads layered configuration for Go services.
//
// Configuration is read from one or more sources (files, directories,
// environment variables, HTTP endpoints or custom layers), merged according to
// the hierarchical model described in [Loader], and decoded into Go structs.
//
// # Basic usage
//
//	loader, err := fastconf.New(
//		fastconf.WithConfigPath("/etc/billing"),
//		fastconf.WithSubsystem("payments"),
//		fastconf.WithApplication("billing"),
//		fastconf.WithEnvironment("production"),
//	)
//	if err != nil {
//		return err
//	}
//
//	var cfg Config
//	if err := loader.Decode(ctx, &cfg); err != nil {
//		return err
//	}
//
// # Configuration model
//
// A document may wrap its values in a "config" object; bare documents are
// wrapped automatically. Reserved sections inside the configuration root bind
// values to subsystems, applications, environments and tenants:
//
//	config:
//	  database:
//	    host: localhost
//	  subsystems:
//	    payments:
//	      database:
//	        host: payments-db
//	  tenants:
//	    acme:
//	      database:
//	        host: acme-db
//	  environments:
//	    production:
//	      database:
//	        host: prod-db
//
// The loader collapses the active subsystem, application and environment, then
// gives every tenant a complete copy of the root configuration. Tenant values
// take precedence over root values.
package fastconf

import (
	"log/slog"
	"net/http"

	"github.com/edanko/fastconf/hook"
	"github.com/edanko/fastconf/layers"
	"github.com/edanko/fastconf/retry"
)

// Option configures a Loader.
type Option func(*options)

type options struct {
	configPath  string
	application string
	subsystem   string
	environment string

	defaultTenant string

	envEnabled   bool
	envPrefix    string
	envSeparator string

	sources    []layers.Layer
	httpClient *http.Client
	logger     *slog.Logger

	remote           *RemoteConfig
	bootstrapEnabled bool
	bootstrapPath    string
	bootstrap        *Bootstrap
	retryPolicy      *retry.Policy

	decoderConfig DecoderConfig
	validator     func(any) error
}

func defaultOptions() options {
	return options{
		defaultTenant: "default",
		envEnabled:    true,
		envPrefix:     "CONFIG",
		envSeparator:  "__",
		decoderConfig: defaultDecoderConfig(),
	}
}

// WithConfigPath sets the configuration file or directory path.
//
// A file is read directly. A directory is read using the hierarchical layout:
//
//	*.yaml, *.yml, *.json, *.toml
//	subsystems/<subsystem>/
//	subsystems/<subsystem>/applications/<application>/
//	tenants/<tenant>/
//	tenants/<tenant>/subsystems/<subsystem>/
//	environments/<environment>/
//	environments/<environment>/subsystems/<subsystem>/
//	environments/<environment>/subsystems/<subsystem>/applications/<application>/
//	environments/<environment>/tenants/<tenant>/
//	environments/<environment>/tenants/<tenant>/subsystems/<subsystem>/
//
// Files are merged in the order above and in lexical order inside each
// directory.
func WithConfigPath(path string) Option {
	return func(options *options) {
		options.configPath = path
	}
}

// WithApplication sets the active application name.
func WithApplication(application string) Option {
	return func(options *options) {
		options.application = application
	}
}

// WithSubsystem sets the active subsystem name.
func WithSubsystem(subsystem string) Option {
	return func(options *options) {
		options.subsystem = subsystem
	}
}

// WithEnvironment sets the active environment name.
func WithEnvironment(environment string) Option {
	return func(options *options) {
		options.environment = environment
	}
}

// WithDefaultTenant sets the tenant code that always exists in the
// configuration. The default is "default". An empty value disables the
// implicit tenant.
func WithDefaultTenant(tenantCode string) Option {
	return func(options *options) {
		options.defaultTenant = tenantCode
	}
}

// WithoutDefaultTenant disables the implicit default tenant.
func WithoutDefaultTenant() Option {
	return WithDefaultTenant("")
}

// WithEnvironmentVariables enables environment variable overrides with the
// default prefix "CONFIG" and separator "__".
func WithEnvironmentVariables() Option {
	return func(options *options) {
		options.envEnabled = true
	}
}

// WithoutEnvironmentVariables disables environment variable overrides.
func WithoutEnvironmentVariables() Option {
	return func(options *options) {
		options.envEnabled = false
	}
}

// WithEnvPrefix sets the environment variable prefix. The default is
// "CONFIG". With prefix "FASTCONF" the variable FASTCONF_DATABASE__HOST sets
// the configuration value database.host.
func WithEnvPrefix(prefix string) Option {
	return func(options *options) {
		options.envPrefix = prefix
		options.envEnabled = true
	}
}

// WithEnvSeparator sets the environment variable path separator. The default
// is "__".
func WithEnvSeparator(separator string) Option {
	return func(options *options) {
		if separator != "" {
			options.envSeparator = separator
		}
	}
}

// WithSources appends custom configuration sources. Sources run after the file
// sources and before environment variable overrides.
func WithSources(sources ...layers.Layer) Option {
	return func(options *options) {
		options.sources = append(options.sources, sources...)
	}
}

// WithLayer appends a single custom configuration source.
func WithLayer(source layers.Layer) Option {
	return WithSources(source)
}

// WithHTTPClient sets the HTTP client used by remote sources.
func WithHTTPClient(client *http.Client) Option {
	return func(options *options) {
		options.httpClient = client
	}
}

// WithLogger sets the logger used for diagnostics.
func WithLogger(logger *slog.Logger) Option {
	return func(options *options) {
		options.logger = logger
	}
}

// WithRemote configures a remote HTTP configuration source directly.
func WithRemote(config RemoteConfig) Option {
	return func(options *options) {
		remote := config
		options.remote = &remote
	}
}

// WithBootstrapPath enables loading a bootstrap document that configures the
// remote source. An empty path uses "bootstrap.yml" next to the configuration
// path.
func WithBootstrapPath(path string) Option {
	return func(options *options) {
		options.bootstrapEnabled = true
		options.bootstrapPath = path
	}
}

// WithBootstrap configures the remote source from an already loaded bootstrap.
func WithBootstrap(bootstrap *Bootstrap) Option {
	return func(options *options) {
		options.bootstrap = bootstrap
	}
}

// WithRetry sets the retry policy used by remote sources.
func WithRetry(policy retry.Policy) Option {
	return func(options *options) {
		options.retryPolicy = &policy
	}
}

// WithDecodeHook appends a decode hook.
func WithDecodeHook(decodeHook hook.Func) Option {
	return func(options *options) {
		options.decoderConfig.DecodeHooks = append(options.decoderConfig.DecodeHooks, decodeHook)
	}
}

// WithDecoderConfig mutates the decoder configuration.
func WithDecoderConfig(configure func(*DecoderConfig)) Option {
	return func(options *options) {
		configure(&options.decoderConfig)
	}
}

// WithStrictDecoding makes decoding fail on configuration keys that have no
// matching struct field.
func WithStrictDecoding() Option {
	return func(options *options) {
		options.decoderConfig.Strict = true
	}
}

// WithValidator sets a custom validator called after successful decoding.
func WithValidator(validator func(any) error) Option {
	return func(options *options) {
		options.validator = validator
	}
}
