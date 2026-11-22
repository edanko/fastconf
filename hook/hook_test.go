package hook_test

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/edanko/fastconf/hook"
)

// apply runs a hook with data as the source value and returns the result.
func apply(fn hook.Func, to reflect.Type, data any) (any, error) {
	return fn(reflect.TypeOf(data), to, data)
}

// declined reports whether the hook returned the value unchanged.
func declined(fn hook.Func, to reflect.Type, data any) bool {
	out, err := apply(fn, to, data)
	if err != nil {
		return false
	}
	return reflect.TypeOf(out) == reflect.TypeOf(data) && reflect.DeepEqual(out, data)
}

func TestStringToTimeDuration(t *testing.T) {
	fn := hook.StringToTimeDuration()
	durationType := reflect.TypeFor[time.Duration]()

	out, err := apply(fn, durationType, "1h30m")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != 90*time.Minute {
		t.Fatalf("out = %v", out)
	}

	if !declined(fn, reflect.TypeFor[int](), "5") {
		t.Fatal("hook should decline non-duration destinations")
	}
	if !declined(fn, durationType, 5) {
		t.Fatal("hook should decline non-string values")
	}

	if _, err := apply(fn, durationType, "soon"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestStringToTime(t *testing.T) {
	timeType := reflect.TypeFor[time.Time]()

	out, err := apply(hook.StringToTime(), timeType, "2024-05-06T07:08:09Z")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	parsed, ok := out.(time.Time)
	if !ok || parsed.Year() != 2024 {
		t.Fatalf("out = %#v", out)
	}

	out, err = apply(hook.StringToTime(), timeType, "2024-05-06")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.(time.Time).Day() != 6 {
		t.Fatalf("out = %v", out)
	}

	custom := hook.StringToTime("02/01/2006")
	out, err = apply(custom, timeType, "06/05/2024")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.(time.Time).Month() != time.May {
		t.Fatalf("out = %v", out)
	}

	if _, err := apply(custom, timeType, "2024-05-06"); err == nil {
		t.Fatal("expected a parse error")
	} else if !strings.Contains(err.Error(), "02/01/2006") {
		t.Fatalf("error %v does not list the accepted layout", err)
	}

	if !declined(hook.StringToTime(), reflect.TypeFor[string](), "text") {
		t.Fatal("hook should decline non-time destinations")
	}
}

func TestStringToURL(t *testing.T) {
	urlType := reflect.TypeFor[url.URL]()
	urlPointerType := reflect.TypeFor[*url.URL]()

	out, err := apply(hook.StringToURL(), urlType, "https://example.test/path?q=1")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	parsed, ok := out.(url.URL)
	if !ok || parsed.Host != "example.test" || parsed.Path != "/path" {
		t.Fatalf("out = %#v", out)
	}

	out, err = apply(hook.StringToURL(), urlPointerType, "https://example.test/")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if pointer, ok := out.(*url.URL); !ok || pointer.Host != "example.test" {
		t.Fatalf("out = %#v", out)
	}

	if _, err := apply(hook.StringToURL(), urlType, "http://[::1"); err == nil {
		t.Fatal("expected a parse error")
	}
	if !declined(hook.StringToURL(), reflect.TypeFor[int](), "x") {
		t.Fatal("hook should decline non-URL destinations")
	}
}

func TestStringToSlice(t *testing.T) {
	tests := []struct {
		name      string
		separator string
		to        reflect.Type
		data      any
		want      any
	}{
		{
			name: "default separator",
			to:   reflect.TypeFor[[]string](),
			data: "a, b,c",
			want: []string{"a", "b", "c"},
		},
		{
			name:      "custom separator",
			separator: "|",
			to:        reflect.TypeFor[[]string](),
			data:      "a|b",
			want:      []string{"a", "b"},
		},
		{
			name: "integers",
			to:   reflect.TypeFor[[]int](),
			data: "1, 2,3",
			want: []int{1, 2, 3},
		},
		{
			name: "booleans",
			to:   reflect.TypeFor[[]bool](),
			data: "true,false",
			want: []bool{true, false},
		},
		{
			name: "floats",
			to:   reflect.TypeFor[[]float64](),
			data: "1.5,2",
			want: []float64{1.5, 2},
		},
		{
			name: "durations",
			to:   reflect.TypeFor[[]time.Duration](),
			data: "1s,2m",
			want: []time.Duration{time.Second, 2 * time.Minute},
		},
		{
			name: "times",
			to:   reflect.TypeFor[[]time.Time](),
			data: "2024-05-06,2025-01-02",
			want: []time.Time{
				time.Date(2024, time.May, 6, 0, 0, 0, 0, time.UTC),
				time.Date(2025, time.January, 2, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "any",
			to:   reflect.TypeFor[[]any](),
			data: "x,y",
			want: []any{"x", "y"},
		},
		{
			name: "array",
			to:   reflect.TypeFor[[2]int](),
			data: "7,8",
			want: [2]int{7, 8},
		},
		{
			name: "empty string",
			to:   reflect.TypeFor[[]string](),
			data: "",
			want: []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := apply(hook.StringToSlice(test.separator), test.to, test.data)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(out, test.want) {
				t.Fatalf("out = %#v, want %#v", out, test.want)
			}
		})
	}

	fn := hook.StringToSlice(",")
	if !declined(fn, reflect.TypeFor[string](), "a,b") {
		t.Fatal("hook should decline non-container destinations")
	}
	if !declined(fn, reflect.TypeFor[[]string](), 5) {
		t.Fatal("hook should decline non-string values")
	}
	if _, err := apply(fn, reflect.TypeFor[[]int](), "1,x"); err == nil {
		t.Fatal("expected a conversion error")
	}
	if _, err := apply(fn, reflect.TypeFor[[2]int](), "1,2,3"); err == nil {
		t.Fatal("expected a length error")
	}
	if _, err := apply(fn, reflect.TypeOf([]struct{}{}), "a,b"); err == nil {
		t.Fatal("expected an unsupported element error")
	}
}

func TestComposeOrder(t *testing.T) {
	var order []string
	first := func(from, to reflect.Type, data any) (any, error) {
		order = append(order, "first")
		return data, nil
	}
	second := func(from, to reflect.Type, data any) (any, error) {
		order = append(order, "second")
		return data, nil
	}

	composed := hook.Compose(first, second, nil)
	if _, err := apply(composed, reflect.TypeFor[int](), "x"); err != nil {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("order = %#v", order)
	}
}

func TestComposeStopsAtDestinationType(t *testing.T) {
	durationType := reflect.TypeFor[time.Duration]()
	stop := func(from, to reflect.Type, data any) (any, error) {
		return time.Second, nil
	}
	never := func(from, to reflect.Type, data any) (any, error) {
		t.Fatal("composition should have stopped")
		return data, nil
	}

	out, err := apply(hook.Compose(stop, never), durationType, "1s")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != time.Second {
		t.Fatalf("out = %v", out)
	}
}

func TestComposeFeedsConvertedValue(t *testing.T) {
	hookOne := func(from, to reflect.Type, data any) (any, error) {
		return strings.ToUpper(data.(string)), nil
	}
	hookTwo := func(from, to reflect.Type, data any) (any, error) {
		return len(data.(string)), nil
	}
	composed := hook.Compose(hookOne, hookTwo)

	out, err := apply(composed, reflect.TypeFor[int](), "abc")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != 3 {
		t.Fatalf("out = %#v", out)
	}
}

func TestComposeNilHandling(t *testing.T) {
	nothing := hook.Compose()
	if !declined(nothing, reflect.TypeFor[string](), "value") {
		t.Fatal("empty composition should keep the value")
	}

	clearAll := func(from, to reflect.Type, data any) (any, error) { return nil, nil }
	after := func(from, to reflect.Type, data any) (any, error) {
		t.Fatal("composition should stop after a nil result")
		return data, nil
	}
	out, err := apply(hook.Compose(clearAll, after), reflect.TypeFor[int](), "value")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != nil {
		t.Fatalf("out = %#v", out)
	}
}

func TestComposePropagatesErrors(t *testing.T) {
	failing := func(from, to reflect.Type, data any) (any, error) {
		return nil, errBoom
	}
	_, err := apply(hook.Compose(failing), reflect.TypeFor[int](), "x")
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v", err)
	}
}

type errType string

func (e errType) Error() string { return string(e) }

var errBoom = errType("boom")

func TestDefault(t *testing.T) {
	if hook.Default == nil {
		t.Fatal("Default must not be nil")
	}
	durationType := reflect.TypeFor[time.Duration]()
	out, err := apply(hook.Default, durationType, "5s")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != 5*time.Second {
		t.Fatalf("duration = %v", out)
	}

	timeType := reflect.TypeFor[time.Time]()
	out, err = apply(hook.Default, timeType, "2024-01-02")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.(time.Time).Year() != 2024 {
		t.Fatalf("time = %v", out)
	}

	urlType := reflect.TypeFor[url.URL]()
	out, err = apply(hook.Default, urlType, "https://example.test/")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.(url.URL).Host != "example.test" {
		t.Fatalf("url = %#v", out)
	}
}

func TestDefaultDoesNotOverrideOtherTypes(t *testing.T) {
	if !declined(hook.Default, reflect.TypeFor[int](), "42") {
		t.Fatal("Default must not convert unrelated destinations")
	}
	if !declined(hook.Default, reflect.TypeFor[url.URL](), 42) {
		t.Fatal("Default must not convert unrelated sources")
	}
}
