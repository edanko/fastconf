package fastconf

import (
	"context"
	"time"

	"github.com/edanko/fastconf/auth"
	"github.com/edanko/fastconf/retry"
)

// Bootstrap configures remote configuration loading. It is usually stored in
// the bootstrap document:
//
//	config:
//	  remote:
//	    url: https://config.example.com/config
//	    timeout: 5s
//	    headers:
//	      X-Environment: production
//	    auth:
//	      type: bearer
//	      token: secret
//	    retry:
//	      maxAttempts: 6
//	      initialInterval: 1s
//	      maxInterval: 10s
//	      multiplier: 2
//	    cacheDir: /var/cache/myapp
//	    useStale: true
type Bootstrap struct {
	// Remote configures the remote configuration source.
	Remote *RemoteConfig `mapstructure:"remote"`
}

// RemoteConfig configures an HTTP configuration source.
type RemoteConfig struct {
	// URL is the absolute HTTP(S) URL of the configuration document.
	URL string `mapstructure:"url"`

	// Timeout is the per-request timeout.
	Timeout time.Duration `mapstructure:"timeout"`

	// Headers are additional HTTP request headers.
	Headers map[string]string `mapstructure:"headers"`

	// Auth configures request authentication.
	Auth auth.Config `mapstructure:"auth"`

	// Retry configures retries. A nil value uses the default policy.
	Retry *retry.Policy `mapstructure:"retry"`

	// CacheDir stores the last successfully fetched document so it can be used
	// when the remote source is unavailable.
	CacheDir string `mapstructure:"cacheDir"`

	// UseStale serves the cached document when fetching fails.
	UseStale bool `mapstructure:"useStale"`

	// FailFast disables retries.
	FailFast bool `mapstructure:"failFast"`
}

// LoadBootstrap loads a bootstrap document without creating a loader. It
// accepts the same options as New; the bootstrap path is taken from
// WithBootstrapPath and defaults to "bootstrap.yml" next to the configuration
// path.
func LoadBootstrap(ctx context.Context, optionList ...Option) (*Bootstrap, error) {
	opts := defaultOptions()
	for _, option := range optionList {
		if option != nil {
			option(&opts)
		}
	}
	return loadBootstrapDocument(ctx, opts)
}
