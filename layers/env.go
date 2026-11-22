package layers

import (
	"context"
	"os"
	"strings"

	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
)

// Env is a configuration source that reads environment variables.
//
// Variables are matched by prefix, for example with prefix "FASTCONF" and
// separator "__" the variable FASTCONF_DATABASE__HOST sets the configuration
// value database.host. Matching of the prefix and of path segments is
// case-insensitive.
type Env struct {
	prefix    string
	separator string
	environ   func() []string
}

// EnvOption configures an Env source.
type EnvOption func(*Env)

// WithEnvSeparator sets the separator used to split nested path segments.
// The default is "__".
func WithEnvSeparator(separator string) EnvOption {
	return func(e *Env) {
		if separator != "" {
			e.separator = separator
		}
	}
}

// WithEnvLookup replaces the environment lookup function. It is primarily
// useful for tests.
func WithEnvLookup(lookup func() []string) EnvOption {
	return func(e *Env) {
		if lookup != nil {
			e.environ = lookup
		}
	}
}

// NewEnv creates an environment variable source.
func NewEnv(prefix string, options ...EnvOption) *Env {
	source := &Env{
		prefix:    prefix,
		separator: "__",
		environ:   os.Environ,
	}
	for _, option := range options {
		option(source)
	}
	return source
}

// Load implements Layer.
func (e *Env) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	document := doc.Normalize(values)
	root, _ := doc.Root(document)

	prefix := e.prefix
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}
	loweredPrefix := strings.ToLower(prefix)

	for _, entry := range e.environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		loweredName := strings.ToLower(name)
		if loweredPrefix != "" && !strings.HasPrefix(loweredName, loweredPrefix) {
			continue
		}
		remainder := name[len(prefix):]
		if remainder == "" {
			continue
		}
		path := strings.Split(remainder, e.separator)
		for i, part := range path {
			path[i] = strings.ToLower(part)
		}
		if err := maps.SetFold(root, path, value); err != nil {
			return nil, err
		}
	}
	document[sections.Root] = root
	return document, nil
}
