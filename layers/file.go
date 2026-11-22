package layers

import (
	"context"
	"fmt"
	"os"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/filereader"
	"github.com/edanko/fastconf/internal/maps"
)

// File is a configuration source that reads one file from the file system.
//
// The encoding is detected from the file extension; WithFileEncoding overrides
// the detection. The document is normalized and merged into the incoming
// document, so later sources still override earlier ones.
type File struct {
	path string
	// encoding overrides extension detection when it is not empty.
	encoding encoding.Encoding
}

// FileOption configures a File source.
type FileOption func(*File)

// WithFileEncoding overrides the encoding detected from the file extension.
// It is useful for files whose extension does not describe their content.
func WithFileEncoding(enc encoding.Encoding) FileOption {
	return func(f *File) {
		f.encoding = enc
	}
}

// NewFile creates a source for the file at path. The file is read on every
// load, so a later load observes changes made on disk.
func NewFile(path string, options ...FileOption) *File {
	source := &File{path: path}
	for _, option := range options {
		option(source)
	}
	return source
}

// Load implements Layer.
func (f *File) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	document := doc.Normalize(values)
	file, err := os.Open(f.path)
	if err != nil {
		return nil, fmt.Errorf("layers: file: %w", err)
	}
	// The file is only read, so a close error cannot lose configuration data.
	defer func() { _ = file.Close() }()

	loaded, err := filereader.DecodeReader(file, f.path, f.encoding)
	if err != nil {
		return nil, fmt.Errorf("layers: file: %w", err)
	}
	maps.Merge(document, loaded)
	return document, nil
}
