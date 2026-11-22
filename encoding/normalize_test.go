package encoding

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	timestamp := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{
			name: "string-keyed objects are converted in place",
			input: map[string]any{
				"nested": map[any]any{
					"list": []any{
						map[any]any{1: "one"},
					},
				},
			},
			want: map[string]any{
				"nested": map[string]any{
					"list": []any{
						map[string]any{"1": "one"},
					},
				},
			},
		},
		{
			name:  "slices of objects become lists of objects",
			input: []map[string]any{{"a": 1}, {"b": 2}},
			want:  []any{map[string]any{"a": 1}, map[string]any{"b": 2}},
		},
		{
			name:  "typed slices become lists",
			input: map[string]any{"ports": []int{80, 443}},
			want:  map[string]any{"ports": []any{80, 443}},
		},
		{
			name:  "typed maps become objects",
			input: map[string]any{"labels": map[string]string{"tier": "gold"}},
			want:  map[string]any{"labels": map[string]any{"tier": "gold"}},
		},
		{
			name:  "integer-keyed maps become objects",
			input: map[any]any{1: "one", "two": 2},
			want:  map[string]any{"1": "one", "two": 2},
		},
		{
			name:  "byte slices stay scalar",
			input: []byte("binary"),
			want:  []byte("binary"),
		},
		{
			name: "scalars keep their exact type",
			input: map[string]any{
				"string":    "value",
				"bool":      true,
				"int":       7,
				"int64":     int64(8),
				"uint64":    uint64(9),
				"float":     1.5,
				"timestamp": timestamp,
			},
			want: map[string]any{
				"string":    "value",
				"bool":      true,
				"int":       7,
				"int64":     int64(8),
				"uint64":    uint64(9),
				"float":     1.5,
				"timestamp": timestamp,
			},
		},
		{
			name:  "untyped nil stays nil",
			input: nil,
			want:  nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalize(test.input)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalize() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestObjectRoot(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    map[string]any
		wantErr string
	}{
		{
			name:  "nil is an empty document",
			input: nil,
			want:  map[string]any{},
		},
		{
			name:  "string-keyed object is kept",
			input: map[string]any{"a": 1},
			want:  map[string]any{"a": 1},
		},
		{
			name:  "interface-keyed object is converted",
			input: map[any]any{1: "one", "two": 2},
			want:  map[string]any{"1": "one", "two": 2},
		},
		{
			name:    "list root is rejected",
			input:   []any{1, 2},
			wantErr: "list",
		},
		{
			name:    "scalar root is rejected",
			input:   "text",
			wantErr: "scalar",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := objectRoot(test.input)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("objectRoot() error = nil, want error containing %q", test.wantErr)
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("objectRoot() error = %q, want it to contain %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("objectRoot() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("objectRoot() = %#v, want %#v", got, test.want)
			}
		})
	}
}
