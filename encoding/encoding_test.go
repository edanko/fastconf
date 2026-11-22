package encoding_test

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/edanko/fastconf/encoding"
)

func TestDecodeYAML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]any
		wantErr string
	}{
		{
			name: "nested objects and lists",
			input: `
name: billing
replicas: 3
enabled: true
database:
  host: localhost
  ports: [5432, 5433]
  pool:
    maxOpen: 10
features:
  - name: invoices
    weight: 1.5
  - name: reports
`,
			want: map[string]any{
				"name":     "billing",
				"replicas": 3,
				"enabled":  true,
				"database": map[string]any{
					"host":  "localhost",
					"ports": []any{5432, 5433},
					"pool":  map[string]any{"maxOpen": 10},
				},
				"features": []any{
					map[string]any{"name": "invoices", "weight": 1.5},
					map[string]any{"name": "reports"},
				},
			},
		},
		{
			name:  "non-string keys are stringified",
			input: "1: one\nlabels:\n  2: two\n",
			want: map[string]any{
				"1":      "one",
				"labels": map[string]any{"2": "two"},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]any{},
		},
		{
			name:  "null document",
			input: "null\n",
			want:  map[string]any{},
		},
		{
			name:  "trailing empty document",
			input: "name: billing\n---\n",
			want:  map[string]any{"name": "billing"},
		},
		{
			name:    "multiple documents",
			input:   "a: 1\n---\nb: 2\n",
			wantErr: "multiple YAML documents",
		},
		{
			name:    "list root",
			input:   "- one\n- two\n",
			wantErr: "want an object",
		},
		{
			name:    "scalar root",
			input:   "hello\n",
			wantErr: "want an object",
		},
		{
			name:    "invalid syntax",
			input:   "name: [unterminated\n",
			wantErr: "yaml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encoding.Decode(strings.NewReader(test.input), encoding.YAML)
			assertDecoded(t, got, err, test.want, test.wantErr)
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]any
		wantErr string
	}{
		{
			name: "nested objects and lists",
			input: `{
				"name": "billing",
				"replicas": 3,
				"enabled": true,
				"database": {"host": "localhost", "ports": [5432, 5433]},
				"features": [{"name": "invoices", "weight": 1.5}]
			}`,
			want: map[string]any{
				"name":     "billing",
				"replicas": float64(3),
				"enabled":  true,
				"database": map[string]any{"host": "localhost", "ports": []any{float64(5432), float64(5433)}},
				"features": []any{map[string]any{"name": "invoices", "weight": 1.5}},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]any{},
		},
		{
			name:  "null document",
			input: "null",
			want:  map[string]any{},
		},
		{
			name:    "trailing data",
			input:   `{"a": 1}{"b": 2}`,
			wantErr: "unexpected data after the document",
		},
		{
			name:    "array root",
			input:   `[1, 2]`,
			wantErr: "want an object",
		},
		{
			name:    "scalar root",
			input:   `"text"`,
			wantErr: "want an object",
		},
		{
			name:    "invalid syntax",
			input:   `{"a": }`,
			wantErr: "invalid character",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encoding.Decode(strings.NewReader(test.input), encoding.JSON)
			assertDecoded(t, got, err, test.want, test.wantErr)
		})
	}
}

func TestDecodeTOML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]any
		wantErr string
	}{
		{
			name: "tables and arrays of tables",
			input: `
title = "billing"
replicas = 3
enabled = true

[owner]
name = "team"

[features]
names = ["a", "b"]

[[services]]
name = "primary"
ports = [80, 443]

[[services]]
name = "fallback"
ports = [8080]
`,
			want: map[string]any{
				"title":    "billing",
				"replicas": int64(3),
				"enabled":  true,
				"owner":    map[string]any{"name": "team"},
				"features": map[string]any{"names": []any{"a", "b"}},
				"services": []any{
					map[string]any{"name": "primary", "ports": []any{int64(80), int64(443)}},
					map[string]any{"name": "fallback", "ports": []any{int64(8080)}},
				},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]any{},
		},
		{
			name:    "invalid syntax",
			input:   "name = \n",
			wantErr: "toml",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encoding.Decode(strings.NewReader(test.input), encoding.TOML)
			assertDecoded(t, got, err, test.want, test.wantErr)
		})
	}
}

func TestDecodeUnsupportedFormat(t *testing.T) {
	_, err := encoding.Decode(strings.NewReader("a = 1"), encoding.Encoding("ini"))
	if err == nil {
		t.Fatal("Decode() error = nil, want an unsupported format error")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("Decode() error = %q, want it to name the unsupported format", err)
	}
}

func TestDecodeNilReader(t *testing.T) {
	_, err := encoding.Decode(nil, encoding.YAML)
	if err == nil || !strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("Decode() error = %v, want a nil reader error", err)
	}
}

func TestFromFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     encoding.Encoding
		wantErr  string
	}{
		{name: "yaml", filename: "config.yaml", want: encoding.YAML},
		{name: "yml", filename: "config.yml", want: encoding.YAML},
		{name: "json", filename: "config.json", want: encoding.JSON},
		{name: "toml", filename: "config.toml", want: encoding.TOML},
		{name: "uppercase extension", filename: "CONFIG.YAML", want: encoding.YAML},
		{name: "directory prefix", filename: "/etc/app/conf/config.yaml", want: encoding.YAML},
		{name: "double extension", filename: "config.local.json", want: encoding.JSON},
		{name: "no extension", filename: "config", wantErr: "no file extension"},
		{name: "unsupported extension", filename: "config.ini", wantErr: "unsupported file extension"},
		{name: "unknown file", filename: "Makefile", wantErr: "no file extension"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encoding.FromFilename(test.filename)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("FromFilename(%q) error = nil, want error containing %q", test.filename, test.wantErr)
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("FromFilename(%q) error = %q, want it to contain %q", test.filename, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromFilename(%q) error = %v", test.filename, err)
			}
			if got != test.want {
				t.Fatalf("FromFilename(%q) = %q, want %q", test.filename, got, test.want)
			}
		})
	}
}

func TestSupportedFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{filename: "config.yaml", want: true},
		{filename: "config.json", want: true},
		{filename: "config.toml", want: true},
		{filename: "config.ini", want: false},
		{filename: "config", want: false},
	}
	for _, test := range tests {
		if got := encoding.SupportedFilename(test.filename); got != test.want {
			t.Fatalf("SupportedFilename(%q) = %t, want %t", test.filename, got, test.want)
		}
	}
}

func TestRegister(t *testing.T) {
	const custom encoding.Encoding = "test-format"

	encoding.Register(custom, func(reader io.Reader) (map[string]any, error) {
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		// A custom decoder may use non-canonical container types; Decode must
		// normalize them.
		return map[string]any{
			"raw":    string(data),
			"nested": map[any]any{1: "one"},
		}, nil
	})

	got, err := encoding.Decode(strings.NewReader("payload"), custom)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	want := map[string]any{
		"raw":    "payload",
		"nested": map[string]any{"1": "one"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}
}

func TestRegisterNilDecoder(t *testing.T) {
	const ignored encoding.Encoding = "test-nil-decoder"

	encoding.Register(ignored, nil)

	_, err := encoding.Decode(strings.NewReader("payload"), ignored)
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("Decode() error = %v, want an unsupported format error for a nil decoder", err)
	}
}

func TestRegisterExtension(t *testing.T) {
	const custom encoding.Encoding = "test-extension-format"

	encoding.Register(custom, func(io.Reader) (map[string]any, error) {
		return map[string]any{}, nil
	})
	encoding.RegisterExtension(".testext", custom)

	detected, err := encoding.FromFilename("config.testext")
	if err != nil {
		t.Fatalf("FromFilename() error = %v", err)
	}
	if detected != custom {
		t.Fatalf("FromFilename() = %q, want %q", detected, custom)
	}
	if !encoding.SupportedFilename("nested/config.testext") {
		t.Fatal("SupportedFilename() = false, want true for a registered extension")
	}

	// An empty extension must not become the mapping for names without one.
	encoding.RegisterExtension(".", encoding.TOML)
	if _, err := encoding.FromFilename("config"); err == nil {
		t.Fatal("FromFilename() error = nil, want names without extension to stay unsupported")
	}
}

// assertDecoded compares a decoded document with the expected one, or checks
// the error message when an error is expected.
func assertDecoded(t *testing.T, got map[string]any, err error, want map[string]any, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil {
			t.Fatalf("Decode() error = nil, want error containing %q", wantErr)
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Decode() error = %q, want it to contain %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}
}
