package layers_test

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/layers"
)

func TestGlobLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"config/a.yaml":     &fstest.MapFile{Data: []byte("database:\n  host: a\n  user: from-a\n")},
		"config/b.yaml":     &fstest.MapFile{Data: []byte("database:\n  host: b\n")},
		"config/empty.yaml": &fstest.MapFile{Data: []byte("")},
		"config/extra.json": &fstest.MapFile{Data: []byte(`{"database": {"port": 5432}}`)},
		"other.yaml":        &fstest.MapFile{Data: []byte("unrelated: true\n")},
	}

	source := layers.NewGlob(fsys, "config/*.yaml", "config/*.json")
	values := map[string]any{"config": map[string]any{"kept": "value"}}
	document, err := source.Load(context.Background(), values)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{
		"kept": "value",
		"database": map[string]any{
			"host": "b",
			"user": "from-a",
			"port": float64(5432),
		},
	}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
	if got, want := mapPointer(document), mapPointer(values); got != want {
		t.Fatal("Load() returned a different map, want the incoming document to be merged in place")
	}
}

func TestGlobEmptyMatchIsNotAnError(t *testing.T) {
	fsys := fstest.MapFS{"config/a.yaml": &fstest.MapFile{Data: []byte("a: 1\n")}}
	values := map[string]any{"config": map[string]any{"kept": true}}

	document, err := layers.NewGlob(fsys, "missing/*.yaml", "also/missing/*.json").Load(context.Background(), values)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"kept": true}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want the incoming document unchanged", document)
	}
}

func TestGlobSkipsDirectories(t *testing.T) {
	fsys := fstest.MapFS{"only/dir/deep.yaml": &fstest.MapFile{Data: []byte("a: 1\n")}}

	document, err := layers.NewGlob(fsys, "only/*").Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want directories to be skipped", document)
	}
}

func TestGlobDecodesEachMatchOnce(t *testing.T) {
	const counted encoding.Encoding = "glob-test-counted"

	decodes := 0
	encoding.Register(counted, func(io.Reader) (map[string]any, error) {
		decodes++
		return map[string]any{"counted": true}, nil
	})
	encoding.RegisterExtension(".globcounted", counted)

	fsys := fstest.MapFS{"counted.globcounted": &fstest.MapFile{Data: []byte("ignored")}}
	source := layers.NewGlob(fsys, "*.globcounted", "counted.globcounted")

	document, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if decodes != 1 {
		t.Fatalf("decoded %d times, want a file matched by two patterns to be decoded once", decodes)
	}
	want := map[string]any{"config": map[string]any{"counted": true}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestGlobDoesNotRetainIncomingDocument(t *testing.T) {
	fsys := fstest.MapFS{"config/a.yaml": &fstest.MapFile{Data: []byte("a: 1\n")}}
	assertSourceDoesNotRetain(t, layers.NewGlob(fsys, "config/*.yaml"))
}

func TestGlobErrors(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		pattern string
		wantErr string
	}{
		{
			name:    "unsupported extension",
			fsys:    fstest.MapFS{"config/notes.txt": &fstest.MapFile{Data: []byte("text")}},
			pattern: "config/*.txt",
			wantErr: "config/notes.txt",
		},
		{
			name:    "invalid pattern",
			fsys:    fstest.MapFS{"config/a.yaml": &fstest.MapFile{Data: []byte("a: 1\n")}},
			pattern: "config/[",
			wantErr: "pattern",
		},
		{
			name:    "invalid document",
			fsys:    fstest.MapFS{"config/broken.yaml": &fstest.MapFile{Data: []byte("a: [\n")}},
			pattern: "config/*.yaml",
			wantErr: "config/broken.yaml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := layers.NewGlob(test.fsys, test.pattern).Load(context.Background(), map[string]any{})
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestGlobNilFileSystem(t *testing.T) {
	_, err := layers.NewGlob(nil, "*.yaml").Load(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "nil file system") {
		t.Fatalf("Load() error = %v, want a nil file system error", err)
	}
}

func TestGlobPatternsAreCopied(t *testing.T) {
	fsys := fstest.MapFS{"config/a.yaml": &fstest.MapFile{Data: []byte("a: 1\n")}}
	patterns := []string{"config/*.yaml"}
	source := layers.NewGlob(fsys, patterns...)
	patterns[0] = "missing/*.yaml"

	document, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"a": 1}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want the source to keep its own pattern copy", document)
	}
}
