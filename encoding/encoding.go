// Package encoding decodes configuration documents from supported formats.
//
// A configuration document is an object tree: every nested object is a
// map[string]any, every list is a []any and scalar values are kept as the
// format decoder produced them. Built-in decoders for YAML, JSON and TOML are
// registered at startup; Register and RegisterExtension add new formats.
package encoding

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
)

// Encoding identifies a configuration document format.
type Encoding string

const (
	// YAML is the YAML format.
	YAML Encoding = "yaml"
	// JSON is the JSON format.
	JSON Encoding = "json"
	// TOML is the TOML format.
	TOML Encoding = "toml"
)

// String returns the encoding name.
func (e Encoding) String() string { return string(e) }

// Decoder decodes a single object document from a reader.
//
// The decoder is responsible for rejecting a document whose root is not an
// object. Nested objects and lists may use any concrete map or slice types;
// Decode converts them into the canonical representation.
type Decoder func(io.Reader) (map[string]any, error)

var (
	registryMutex sync.RWMutex
	registry      = map[Encoding]Decoder{
		YAML: decodeYAML,
		JSON: decodeJSON,
		TOML: decodeTOML,
	}
	extensions = map[string]Encoding{
		"yaml": YAML,
		"yml":  YAML,
		"json": JSON,
		"toml": TOML,
	}
)

// Register adds or replaces the decoder of an encoding. A nil decoder is
// ignored, so a format cannot be registered without a way to decode it.
func Register(enc Encoding, decoder Decoder) {
	if decoder == nil {
		return
	}
	registryMutex.Lock()
	registry[enc] = decoder
	registryMutex.Unlock()
}

// RegisterExtension associates a file extension with an encoding. The
// extension may carry a leading dot and is matched case-insensitively. An
// empty extension is ignored.
func RegisterExtension(extension string, enc Encoding) {
	extension = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(extension)), ".")
	if extension == "" {
		return
	}
	registryMutex.Lock()
	extensions[extension] = enc
	registryMutex.Unlock()
}

// Decode decodes a document of the given encoding.
//
// The returned document is always an object: nested objects are converted to
// map[string]any and nested lists to []any, whichever types the decoder used.
// An empty input decodes into an empty document.
func Decode(reader io.Reader, enc Encoding) (map[string]any, error) {
	if reader == nil {
		return nil, fmt.Errorf("encoding: decode %s: nil reader", enc)
	}
	registryMutex.RLock()
	decoder, registered := registry[enc]
	registryMutex.RUnlock()
	if !registered {
		return nil, fmt.Errorf("encoding: unsupported format %q", enc)
	}
	document, err := decoder(reader)
	if err != nil {
		return nil, fmt.Errorf("encoding: decode %s: %w", enc, err)
	}
	if document == nil {
		return map[string]any{}, nil
	}
	return normalize(document).(map[string]any), nil
}

// FromFilename returns the encoding associated with a file name.
//
// The encoding is selected by the file extension, matched case-insensitively.
func FromFilename(name string) (Encoding, error) {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if extension == "" {
		return "", fmt.Errorf("encoding: %q has no file extension", name)
	}
	registryMutex.RLock()
	enc, registered := extensions[extension]
	registryMutex.RUnlock()
	if !registered {
		return "", fmt.Errorf("encoding: unsupported file extension %q", extension)
	}
	return enc, nil
}

// SupportedFilename reports whether the file name has a supported extension.
func SupportedFilename(name string) bool {
	_, err := FromFilename(name)
	return err == nil
}
