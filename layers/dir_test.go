package layers_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/edanko/fastconf/layers"
)

func TestDirLoad(t *testing.T) {
	root := dirTestTree(t)

	document, err := layers.NewDir(root).Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Sorted paths are config.json, nested/a.yaml, skip/b.yaml, z.yaml. The
	// last file to set database.host wins.
	want := map[string]any{"config": map[string]any{"database": map[string]any{
		"port":        float64(5432),
		"user":        "app",
		"skippedOnly": true,
		"host":        "z",
		"shared":      "z",
	}}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestDirNonRecursive(t *testing.T) {
	root := dirTestTree(t)

	document, err := layers.NewDir(root, layers.WithDirRecursive(false)).Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"database": map[string]any{
		"host":   "z",
		"shared": "z",
		"port":   float64(5432),
	}}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestDirFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter func(path string) bool
		want   map[string]any
	}{
		{
			name:   "prunes a directory",
			filter: func(path string) bool { return path != "skip" },
			want: map[string]any{"config": map[string]any{"database": map[string]any{
				"host":   "z",
				"shared": "z",
				"port":   float64(5432),
				"user":   "app",
			}}},
		},
		{
			name:   "skips a file",
			filter: func(path string) bool { return path != "nested/a.yaml" },
			want: map[string]any{"config": map[string]any{"database": map[string]any{
				"host":        "z",
				"shared":      "z",
				"port":        float64(5432),
				"skippedOnly": true,
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := dirTestTree(t)
			source := layers.NewDir(root, layers.WithDirFilter(test.filter))
			document, err := source.Load(context.Background(), nil)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(document, test.want) {
				t.Fatalf("Load() = %#v, want %#v", document, test.want)
			}
		})
	}
}

func TestDirIgnoresUnsupportedFiles(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "notes.txt", "this is not: valid [ configuration")
	writeSourceFile(t, root, "config.yaml", "application: billing\n")

	document, err := layers.NewDir(root).Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"application": "billing"}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestDirEmpty(t *testing.T) {
	document, err := layers.NewDir(t.TempDir()).Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want an empty document", document)
	}
}

func TestDirReloadsChangedFiles(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "config.yaml", "database:\n  host: first\n")
	source := layers.NewDir(root)

	first, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if host := sourceHost(t, first); host != "first" {
		t.Fatalf("Load() host = %q, want %q", host, "first")
	}

	writeSourceFile(t, root, "config.yaml", "database:\n  host: second\n")
	second, err := source.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if host := sourceHost(t, second); host != "second" {
		t.Fatalf("Load() host = %q, want %q", host, "second")
	}
}

func TestDirDoesNotRetainIncomingDocument(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "config.yaml", "database:\n  host: file-host\n")
	assertSourceDoesNotRetain(t, layers.NewDir(root))
}

func TestDirErrors(t *testing.T) {
	root := t.TempDir()
	filePath := writeSourceFile(t, root, "config.yaml", "a: 1\n")
	invalidPath := writeSourceFile(t, root, "invalid.yaml", "a: [\n")

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{
			name:    "missing directory",
			path:    filepath.Join(root, "absent"),
			wantErr: "no such file",
		},
		{
			name:    "not a directory",
			path:    filePath,
			wantErr: "not a directory",
		},
		{
			name:    "invalid document",
			path:    invalidPath,
			wantErr: "invalid.yaml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := layers.NewDir(test.path).Load(context.Background(), nil)
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestDirFilterReceivesRelativeSlashPaths(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "nested/config.yaml", "a: 1\n")

	var seen []string
	filter := func(path string) bool {
		seen = append(seen, path)
		return true
	}
	if _, err := layers.NewDir(root, layers.WithDirFilter(filter)).Load(context.Background(), nil); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"nested", "nested/config.yaml"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("filter paths = %#v, want %#v", seen, want)
	}
}

// dirTestTree creates a directory tree with configuration files and returns its
// root. The file names are chosen so that the sorted load order differs from
// the creation order.
func dirTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeSourceFile(t, root, "z.yaml", "database:\n  host: z\n  shared: z\n")
	writeSourceFile(t, root, "config.json", `{"database": {"port": 5432}}`)
	writeSourceFile(t, root, "nested/a.yaml", "database:\n  host: nested\n  user: app\n")
	writeSourceFile(t, root, "skip/b.yaml", "database:\n  skippedOnly: true\n")
	return root
}
