// Package filereader opens and decodes configuration files.
//
// A file is decoded with the encoding package and normalized into the
// canonical document form, so the result always has a configuration root and
// can be merged into the document of a configuration source.
package filereader

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
)

// DecodeReader decodes one document from reader and normalizes it.
//
// When enc is empty the encoding is detected from the file name, so a name is
// required in that case. The name is used in error messages as well.
func DecodeReader(reader io.Reader, name string, enc encoding.Encoding) (map[string]any, error) {
	if reader == nil {
		return nil, errors.New("nil reader")
	}
	if enc == "" {
		if name == "" {
			return nil, errors.New("no encoding and no file name to detect it from")
		}
		detected, err := encoding.FromFilename(name)
		if err != nil {
			return nil, fmt.Errorf("detect encoding from %s: %w", name, err)
		}
		enc = detected
	}
	document, err := encoding.Decode(reader, enc)
	if err != nil {
		if name == "" {
			return nil, fmt.Errorf("decode: %w", err)
		}
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return doc.Normalize(document), nil
}

// DecodeFile opens name in fsys and decodes it. See DecodeReader for the
// meaning of name and enc.
func DecodeFile(fsys fs.FS, name string, enc encoding.Encoding) (map[string]any, error) {
	if fsys == nil {
		return nil, errors.New("nil file system")
	}
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	// The file is only read, so a close error cannot lose configuration data.
	defer func() { _ = file.Close() }()
	return DecodeReader(file, name, enc)
}

// DecodeFiles decodes every file of names and merges the documents into one
// normalized document.
//
// Files are decoded in sorted name order, so later names override earlier ones
// deterministically, and duplicate names are decoded once. An empty names slice
// produces an empty document.
func DecodeFiles(fsys fs.FS, names []string) (map[string]any, error) {
	if fsys == nil {
		return nil, errors.New("nil file system")
	}
	ordered := slices.Clone(names)
	slices.Sort(ordered)
	ordered = slices.Compact(ordered)
	document := make(map[string]any, len(ordered))
	for _, name := range ordered {
		decoded, err := DecodeFile(fsys, name, "")
		if err != nil {
			return nil, err
		}
		maps.Merge(document, decoded)
	}
	return doc.Normalize(document), nil
}
