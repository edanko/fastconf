package layers

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/filereader"
	"github.com/edanko/fastconf/internal/maps"
)

// Dir is a configuration source that reads the supported files under an OS
// directory.
//
// Files are visited recursively by default and decoded in sorted path order, so
// later paths override earlier ones deterministically. Files whose extension is
// not registered with the encoding package are ignored, so a directory may hold
// documentation next to configuration. A directory without a supported file is
// not an error.
type Dir struct {
	path      string
	filter    func(path string) bool
	recursive bool
}

// DirOption configures a Dir source.
type DirOption func(*Dir)

// WithDirFilter restricts the entries that are loaded.
//
// The filter receives the path of every entry relative to the root directory,
// with forward slashes. Returning false skips a file and prunes a directory
// together with its contents.
func WithDirFilter(filter func(path string) bool) DirOption {
	return func(d *Dir) {
		d.filter = filter
	}
}

// WithDirRecursive controls whether subdirectories are visited. The default is
// true.
func WithDirRecursive(recursive bool) DirOption {
	return func(d *Dir) {
		d.recursive = recursive
	}
}

// NewDir creates a source for the directory at path.
func NewDir(path string, options ...DirOption) *Dir {
	source := &Dir{path: path, recursive: true}
	for _, option := range options {
		option(source)
	}
	return source
}

// Load implements Layer.
func (d *Dir) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	document := doc.Normalize(values)
	paths, err := d.paths()
	if err != nil {
		return nil, err
	}
	loaded, err := filereader.DecodeFiles(os.DirFS(d.path), paths)
	if err != nil {
		return nil, fmt.Errorf("layers: dir %s: %w", d.path, err)
	}
	maps.Merge(document, loaded)
	return document, nil
}

// paths returns the sorted paths of the supported files under the root.
func (d *Dir) paths() ([]string, error) {
	info, err := os.Stat(d.path)
	if err != nil {
		return nil, fmt.Errorf("layers: dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("layers: dir: %s is not a directory", d.path)
	}

	var paths []string
	root := os.DirFS(d.path)
	err = fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if entry.IsDir() {
			if !d.recursive {
				return fs.SkipDir
			}
			if d.filter != nil && !d.filter(path) {
				return fs.SkipDir
			}
			return nil
		}
		if d.filter != nil && !d.filter(path) {
			return nil
		}
		if !encoding.SupportedFilename(path) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("layers: dir: walk %s: %w", d.path, err)
	}
	slices.Sort(paths)
	return paths, nil
}
