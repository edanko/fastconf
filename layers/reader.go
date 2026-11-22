package layers

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/filereader"
	"github.com/edanko/fastconf/internal/maps"
)

// Reader is a configuration source that reads a document from an io.ReadSeeker.
//
// The reader is rewound to its start before every load, so the same source can
// be loaded more than once. The reader is not closed, because its owner is the
// caller.
type Reader struct {
	reader   io.ReadSeeker
	encoding encoding.Encoding
	name     string
}

// ReaderOption configures a Reader source.
type ReaderOption func(*Reader)

// WithReaderName sets the name used in error messages. When the source was
// created with an empty encoding, the name also supplies the file extension
// that the encoding is detected from.
func WithReaderName(name string) ReaderOption {
	return func(r *Reader) {
		r.name = name
	}
}

// NewReader creates a source that reads from reader with the given encoding.
// An empty encoding is detected from the name set by WithReaderName.
func NewReader(reader io.ReadSeeker, enc encoding.Encoding, options ...ReaderOption) *Reader {
	source := &Reader{reader: reader, encoding: enc}
	for _, option := range options {
		option(source)
	}
	return source
}

// Load implements Layer.
func (r *Reader) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	if r.reader == nil {
		return nil, errors.New("layers: reader: nil reader")
	}
	document := doc.Normalize(values)
	if _, err := r.reader.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("layers: reader: seek: %w", err)
	}
	loaded, err := filereader.DecodeReader(r.reader, r.name, r.encoding)
	if err != nil {
		return nil, fmt.Errorf("layers: reader: %w", err)
	}
	maps.Merge(document, loaded)
	return document, nil
}
