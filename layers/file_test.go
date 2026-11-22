package layers_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/layers"
)

func TestFileLoad(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		encoding encoding.Encoding
		want     map[string]any
	}{
		{
			name:     "yaml",
			filename: "config.yaml",
			content:  "database:\n  host: yaml-host\n",
			want:     map[string]any{"config": map[string]any{"database": map[string]any{"host": "yaml-host", "user": "app"}}},
		},
		{
			name:     "json",
			filename: "config.json",
			content:  `{"database": {"host": "json-host"}}`,
			want:     map[string]any{"config": map[string]any{"database": map[string]any{"host": "json-host", "user": "app"}}},
		},
		{
			name:     "toml",
			filename: "config.toml",
			content:  "[database]\nhost = \"toml-host\"\n",
			want:     map[string]any{"config": map[string]any{"database": map[string]any{"host": "toml-host", "user": "app"}}},
		},
		{
			name:     "extension override",
			filename: "config.data",
			content:  "database:\n  host: override-host\n",
			encoding: encoding.YAML,
			want:     map[string]any{"config": map[string]any{"database": map[string]any{"host": "override-host", "user": "app"}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeSourceFile(t, t.TempDir(), test.filename, test.content)

			options := []layers.FileOption{}
			if test.encoding != "" {
				options = append(options, layers.WithFileEncoding(test.encoding))
			}
			source := layers.NewFile(path, options...)

			values := map[string]any{"config": map[string]any{"database": map[string]any{"user": "app"}}}
			document, err := source.Load(context.Background(), values)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(document, test.want) {
				t.Fatalf("Load() = %#v, want %#v", document, test.want)
			}
			// A source merges into the incoming document instead of replacing it.
			if got, want := mapPointer(document), mapPointer(values); got != want {
				t.Fatalf("Load() returned a different map, want the incoming document to be merged in place")
			}
		})
	}
}

func TestFileBareDocumentIsWrapped(t *testing.T) {
	path := writeSourceFile(t, t.TempDir(), "config.yaml", "application: billing\n")
	document, err := layers.NewFile(path).Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"application": "billing"}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestFileEmptyFile(t *testing.T) {
	path := writeSourceFile(t, t.TempDir(), "empty.yaml", "")
	document, err := layers.NewFile(path).Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestFileReadsChangesOnEveryLoad(t *testing.T) {
	dir := t.TempDir()
	path := writeSourceFile(t, dir, "config.yaml", "database:\n  host: first\n")
	source := layers.NewFile(path)

	first, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if host := sourceHost(t, first); host != "first" {
		t.Fatalf("Load() host = %q, want %q", host, "first")
	}

	writeSourceFile(t, dir, "config.yaml", "database:\n  host: second\n")
	second, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if host := sourceHost(t, second); host != "second" {
		t.Fatalf("Load() host = %q, want %q", host, "second")
	}
}

func TestFileDoesNotRetainIncomingDocument(t *testing.T) {
	path := writeSourceFile(t, t.TempDir(), "config.yaml", "database:\n  host: file-host\n")
	assertSourceDoesNotRetain(t, layers.NewFile(path))
}

func TestFileErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{
			name:    "missing file",
			path:    filepath.Join(dir, "absent.yaml"),
			wantErr: "no such file",
		},
		{
			name:    "unsupported extension",
			path:    writeSourceFile(t, dir, "config.ini", "a = 1\n"),
			wantErr: "unsupported file extension",
		},
		{
			name:    "invalid document",
			path:    writeSourceFile(t, dir, "broken.yaml", "database: [unterminated\n"),
			wantErr: "broken.yaml",
		},
		{
			name:    "directory path",
			path:    sourceDirectory(t, dir, "config.yaml"),
			wantErr: "is a directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := layers.NewFile(test.path).Load(context.Background(), map[string]any{})
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

// sourceDirectory creates a directory named name and returns its path.
func sourceDirectory(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create fixture directory %s: %v", name, err)
	}
	return path
}

// writeSourceFile writes a configuration fixture and returns its path.
func writeSourceFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// sourceHost returns database.host of a normalized configuration document.
func sourceHost(t *testing.T, document map[string]any) string {
	t.Helper()
	root, ok := document["config"].(map[string]any)
	if !ok {
		t.Fatalf("document %#v has no configuration root", document)
	}
	database, ok := root["database"].(map[string]any)
	if !ok {
		t.Fatalf("document %#v has no database", document)
	}
	host, _ := database["host"].(string)
	return host
}

// assertSourceDoesNotRetain checks that a source does not carry values of a
// previously loaded document into the next load.
func assertSourceDoesNotRetain(t *testing.T, source layers.Layer) {
	t.Helper()
	first := map[string]any{"config": map[string]any{"onlyInFirst": true}}
	if _, err := source.Load(context.Background(), first); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	document, err := source.Load(context.Background(), map[string]any{"config": map[string]any{"onlyInSecond": true}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	root, ok := document["config"].(map[string]any)
	if !ok {
		t.Fatalf("Load() = %#v, want a configuration root", document)
	}
	if _, retained := root["onlyInFirst"]; retained {
		t.Fatalf("Load() retained a value of the previous document: %#v", document)
	}
	if _, present := root["onlyInSecond"]; !present {
		t.Fatalf("Load() dropped a value of the incoming document: %#v", document)
	}
}

// mapPointer identifies the map behind a map header so tests can assert that a
// source merges into the incoming document.
func mapPointer(values map[string]any) uintptr {
	return reflect.ValueOf(values).Pointer()
}
