package filereader_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/filereader"
)

func TestDecodeReader(t *testing.T) {
	tests := []struct {
		name    string
		reader  *strings.Reader
		fname   string
		enc     encoding.Encoding
		want    map[string]any
		wantErr string
	}{
		{
			name:   "explicit encoding",
			reader: strings.NewReader("database:\n  host: localhost\n"),
			fname:  "config.yaml",
			enc:    encoding.YAML,
			want:   map[string]any{"config": map[string]any{"database": map[string]any{"host": "localhost"}}},
		},
		{
			name:   "encoding detected from name",
			reader: strings.NewReader(`{"database": {"host": "localhost"}}`),
			fname:  "config.json",
			want:   map[string]any{"config": map[string]any{"database": map[string]any{"host": "localhost"}}},
		},
		{
			name:   "already canonical document is kept",
			reader: strings.NewReader("config:\n  database:\n    host: localhost\n"),
			fname:  "config.yaml",
			enc:    encoding.YAML,
			want:   map[string]any{"config": map[string]any{"database": map[string]any{"host": "localhost"}}},
		},
		{
			name:   "empty input normalizes to an empty root",
			reader: strings.NewReader(""),
			fname:  "config.yaml",
			enc:    encoding.YAML,
			want:   map[string]any{"config": map[string]any{}},
		},
		{
			name:    "unsupported extension",
			reader:  strings.NewReader("a = 1"),
			fname:   "config.ini",
			wantErr: "unsupported file extension",
		},
		{
			name:    "no encoding and no name",
			reader:  strings.NewReader("a: 1"),
			wantErr: "no encoding and no file name",
		},
		{
			name:    "decode error names the file",
			reader:  strings.NewReader("database: [unterminated\n"),
			fname:   "config.yaml",
			wantErr: "config.yaml",
		},
		{
			name:    "nil reader",
			fname:   "config.yaml",
			wantErr: "nil reader",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := test.reader
			var got map[string]any
			var err error
			if reader == nil {
				got, err = filereader.DecodeReader(nil, test.fname, test.enc)
			} else {
				got, err = filereader.DecodeReader(reader, test.fname, test.enc)
			}
			assertDocument(t, got, err, test.want, test.wantErr)
		})
	}
}

func TestDecodeFile(t *testing.T) {
	fsys := fstest.MapFS{
		"config.yaml":   &fstest.MapFile{Data: []byte("database:\n  host: file-host\n")},
		"broken.yaml":   &fstest.MapFile{Data: []byte("database: [unterminated\n")},
		"notes.txt":     &fstest.MapFile{Data: []byte("not configuration")},
		"nested/x.json": &fstest.MapFile{Data: []byte(`{"database": {"port": 5432}}`)},
	}

	got, err := filereader.DecodeFile(fsys, "config.yaml", "")
	if err != nil {
		t.Fatalf("DecodeFile() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"database": map[string]any{"host": "file-host"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeFile() = %#v, want %#v", got, want)
	}

	if _, err := filereader.DecodeFile(fsys, "missing.yaml", ""); err == nil {
		t.Fatal("DecodeFile() error = nil, want an error for a missing file")
	}
	if _, err := filereader.DecodeFile(fsys, "notes.txt", ""); err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("DecodeFile() error = %v, want an error naming the unsupported file", err)
	}
	if _, err := filereader.DecodeFile(fsys, "broken.yaml", ""); err == nil || !strings.Contains(err.Error(), "broken.yaml") {
		t.Fatalf("DecodeFile() error = %v, want an error naming the invalid file", err)
	}
	if _, err := filereader.DecodeFile(nil, "config.yaml", encoding.YAML); err == nil {
		t.Fatal("DecodeFile() error = nil, want an error for a nil file system")
	}
}

func TestDecodeFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"b.yaml": &fstest.MapFile{Data: []byte("database:\n  host: second\n  port: 5432\n")},
		"a.yaml": &fstest.MapFile{Data: []byte("database:\n  host: first\n  user: app\n")},
		"c.json": &fstest.MapFile{Data: []byte(`{"database": {"host": "third"}}`)},
	}

	got, err := filereader.DecodeFiles(fsys, []string{"b.yaml", "c.json", "a.yaml", "b.yaml"})
	if err != nil {
		t.Fatalf("DecodeFiles() error = %v", err)
	}
	// Sorted order is a.yaml, b.yaml, c.json; duplicates are decoded once and
	// later files override earlier ones.
	want := map[string]any{"config": map[string]any{
		"database": map[string]any{
			"host": "third",
			"port": 5432,
			"user": "app",
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeFiles() = %#v, want %#v", got, want)
	}

	empty, err := filereader.DecodeFiles(fsys, nil)
	if err != nil {
		t.Fatalf("DecodeFiles() error = %v", err)
	}
	if !reflect.DeepEqual(empty, map[string]any{"config": map[string]any{}}) {
		t.Fatalf("DecodeFiles() = %#v, want an empty normalized document", empty)
	}

	if _, err := filereader.DecodeFiles(fsys, []string{"a.yaml", "missing.yaml"}); err == nil {
		t.Fatal("DecodeFiles() error = nil, want an error for a missing file")
	}
	if _, err := filereader.DecodeFiles(nil, []string{"a.yaml"}); err == nil {
		t.Fatal("DecodeFiles() error = nil, want an error for a nil file system")
	}
}

func assertDocument(t *testing.T, got map[string]any, err error, want map[string]any, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil {
			t.Fatalf("error = nil, want error containing %q", wantErr)
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("error = %q, want it to contain %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("document = %#v, want %#v", got, want)
	}
}
