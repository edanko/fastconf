package layers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/filereader"
	"github.com/edanko/fastconf/internal/maps"
)

// Glob is a configuration source that reads the files matched by its patterns
// inside a file system.
//
// Matches of all patterns are deduplicated and decoded in sorted path order,
// so a file matched by several patterns is read once and later paths override
// earlier ones. Directories matched by a pattern are skipped. Every matched
// file is decoded, so a matched file with an unsupported extension is reported
// as an error rather than ignored. A pattern that matches no file is not an
// error.
type Glob struct {
	fsys     fs.FS
	patterns []string
}

// NewGlob creates a source over fsys for the given fs.Glob patterns. The
// patterns are copied, so later changes to the caller's slice do not affect
// the source.
func NewGlob(fsys fs.FS, patterns ...string) *Glob {
	return &Glob{fsys: fsys, patterns: slices.Clone(patterns)}
}

// Load implements Layer.
func (g *Glob) Load(_ context.Context, values map[string]any) (map[string]any, error) {
	document := doc.Normalize(values)
	paths, err := g.paths()
	if err != nil {
		return nil, err
	}
	loaded, err := filereader.DecodeFiles(g.fsys, paths)
	if err != nil {
		return nil, fmt.Errorf("layers: glob: %w", err)
	}
	maps.Merge(document, loaded)
	return document, nil
}

// paths returns the sorted, deduplicated files matched by the patterns.
func (g *Glob) paths() ([]string, error) {
	if g.fsys == nil {
		return nil, errors.New("layers: glob: nil file system")
	}
	seen := make(map[string]struct{}, len(g.patterns))
	paths := make([]string, 0, len(g.patterns))
	for _, pattern := range g.patterns {
		matches, err := fs.Glob(g.fsys, pattern)
		if err != nil {
			return nil, fmt.Errorf("layers: glob: pattern %q: %w", pattern, err)
		}
		for _, path := range matches {
			if _, duplicate := seen[path]; duplicate {
				continue
			}
			info, err := fs.Stat(g.fsys, path)
			if err != nil {
				return nil, fmt.Errorf("layers: glob: %w", err)
			}
			if info.IsDir() {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths, nil
}
