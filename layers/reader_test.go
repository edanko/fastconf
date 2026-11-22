package layers_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/layers"
)

func TestReaderLoad(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		encoding encoding.Encoding
		want     map[string]any
	}{
		{
			name:     "yaml",
			content:  "database:\n  host: yaml-host\n  ports: [5432, 5433]\n",
			encoding: encoding.YAML,
			want: map[string]any{"config": map[string]any{"database": map[string]any{
				"host":  "yaml-host",
				"ports": []any{5432, 5433},
				"user":  "app",
			}}},
		},
		{
			name:     "json",
			content:  `{"database": {"host": "json-host"}}`,
			encoding: encoding.JSON,
			want: map[string]any{"config": map[string]any{"database": map[string]any{
				"host": "json-host",
				"user": "app",
			}}},
		},
		{
			name:     "toml",
			content:  "[database]\nhost = \"toml-host\"\n",
			encoding: encoding.TOML,
			want: map[string]any{"config": map[string]any{"database": map[string]any{
				"host": "toml-host",
				"user": "app",
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := bytes.NewReader([]byte(test.content))
			source := layers.NewReader(reader, test.encoding)
			values := map[string]any{"config": map[string]any{"database": map[string]any{"user": "app"}}}
			document, err := source.Load(context.Background(), values)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(document, test.want) {
				t.Fatalf("Load() = %#v, want %#v", document, test.want)
			}
			if got, want := mapPointer(document), mapPointer(values); got != want {
				t.Fatal("Load() returned a different map, want the incoming document to be merged in place")
			}
		})
	}
}

func TestReaderRewindsBeforeEveryLoad(t *testing.T) {
	reader := bytes.NewReader([]byte("database:\n  host: localhost\n"))
	source := layers.NewReader(reader, encoding.YAML)

	first, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	second, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Load() = %#v on the second call, want %#v", second, first)
	}
	if position, err := reader.Seek(0, io.SeekCurrent); err != nil || position == 0 {
		t.Fatalf("reader position = %d, error = %v, want a consumed reader", position, err)
	}
}

func TestReaderDetectsEncodingFromName(t *testing.T) {
	reader := bytes.NewReader([]byte(`{"application": "billing"}`))
	source := layers.NewReader(reader, "", layers.WithReaderName("config.json"))

	document, err := source.Load(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]any{"config": map[string]any{"application": "billing"}}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Load() = %#v, want %#v", document, want)
	}
}

func TestReaderDoesNotRetainIncomingDocument(t *testing.T) {
	reader := bytes.NewReader([]byte("database:\n  host: localhost\n"))
	assertSourceDoesNotRetain(t, layers.NewReader(reader, encoding.YAML))
}

func TestReaderErrors(t *testing.T) {
	tests := []struct {
		name    string
		reader  io.ReadSeeker
		enc     encoding.Encoding
		options []layers.ReaderOption
		wantErr string
	}{
		{
			name:    "nil reader",
			reader:  nil,
			enc:     encoding.YAML,
			wantErr: "nil reader",
		},
		{
			name:    "seek failure",
			reader:  seekErrorReader{err: errors.New("seek is not supported")},
			enc:     encoding.YAML,
			wantErr: "seek is not supported",
		},
		{
			name:    "invalid document names the reader",
			reader:  bytes.NewReader([]byte("database: [unterminated\n")),
			enc:     encoding.YAML,
			options: []layers.ReaderOption{layers.WithReaderName("config.yaml")},
			wantErr: "config.yaml",
		},
		{
			name:    "unsupported encoding",
			reader:  bytes.NewReader([]byte("a = 1")),
			enc:     encoding.Encoding("ini"),
			wantErr: "unsupported format",
		},
		{
			name:    "no encoding and no name",
			reader:  bytes.NewReader([]byte("a: 1")),
			wantErr: "no encoding and no file name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := layers.NewReader(test.reader, test.enc, test.options...).Load(context.Background(), map[string]any{})
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

// seekErrorReader is an io.ReadSeeker whose Seek always fails.
type seekErrorReader struct {
	err error
}

func (r seekErrorReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (r seekErrorReader) Seek(int64, int) (int64, error) {
	return 0, r.err
}
