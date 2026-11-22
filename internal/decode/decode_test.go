package decode_test

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edanko/fastconf/hook"
	"github.com/edanko/fastconf/internal/decode"
	"github.com/edanko/fastconf/internal/value"
)

// doc converts a plain map into a configuration value tree.
func doc(values map[string]any) *value.Value {
	v := value.From(values)
	return &v
}

// decodeInto decodes values into dst and fails the test on error.
func decodeInto(t *testing.T, decoder *decode.Decoder, values map[string]any, dst any) {
	t.Helper()
	if err := decoder.Decode(doc(values), dst); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
}

// decodeErr decodes values into dst and returns the error.
func decodeErr(decoder *decode.Decoder, values map[string]any, dst any) error {
	return decoder.Decode(doc(values), dst)
}

func TestDecodeScalars(t *testing.T) {
	type config struct {
		Name    string
		Port    int
		Ratio   float64
		Enabled bool
		Small   int8
		Count   uint
		Ratio32 float32
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"name":    "billing",
		"port":    8080,
		"ratio":   1.5,
		"enabled": true,
		"small":   -7,
		"count":   9000000000,
		"ratio32": 0.25,
	}, &got)

	want := config{Name: "billing", Port: 8080, Ratio: 1.5, Enabled: true, Small: -7, Count: 9000000000, Ratio32: 0.25}
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestCaseInsensitiveFieldMatching(t *testing.T) {
	type config struct {
		HostName string
		MaxConns int
		Debug    bool
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"hostname":  "db.internal",
		"MAXCONNS":  42,
		"DeBuG":     true,
		"unrelated": "ignored",
	}, &got)

	if got.HostName != "db.internal" || got.MaxConns != 42 || !got.Debug {
		t.Fatalf("decoded %+v", got)
	}
}

func TestTagPrecedence(t *testing.T) {
	type config struct {
		Primary  string `mapstructure:"mp" json:"js" yaml:"ym" toml:"tm"`
		JSONOnly string `json:"json_name"`
		YAMLOnly string `yaml:"yaml_name"`
		TOMLOnly string `toml:"toml_name"`
		GoName   string
		Skipped  string `mapstructure:"-"`
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"mp":        "primary",
		"js":        "should not win",
		"json_name": "json",
		"yaml_name": "yaml",
		"toml_name": "toml",
		"goname":    "go",
		"skipped":   "nope",
	}, &got)

	want := config{Primary: "primary", JSONOnly: "json", YAMLOnly: "yaml", TOMLOnly: "toml", GoName: "go"}
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}

	var custom struct {
		Value string `conf:"custom_name" mapstructure:"other"`
	}
	decodeInto(t, decode.New(decode.Config{TagName: "conf"}), map[string]any{"custom_name": "ok"}, &custom)
	if custom.Value != "ok" {
		t.Fatalf("custom tag decoded %+v", custom)
	}
}

func TestTagOptionsIgnored(t *testing.T) {
	type config struct {
		Name string `json:"name,omitempty" yaml:"name,flow"`
	}
	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{"name": "value"}, &got)
	if got.Name != "value" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestEmbeddedFlattening(t *testing.T) {
	type inner struct {
		Host string
		Port int
	}
	type Exported struct {
		Region string
	}
	type config struct {
		inner
		Exported
		Name string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"host":   "localhost",
		"port":   5432,
		"region": "eu",
		"name":   "app",
	}, &got)

	if got.Host != "localhost" || got.Port != 5432 || got.Region != "eu" || got.Name != "app" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestEmbeddedWithExplicitNameIsNested(t *testing.T) {
	type Exported struct {
		Region string
	}
	type config struct {
		Exported `mapstructure:"cloud"`
		Name     string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"cloud": map[string]any{"region": "us"},
		"name":  "app",
	}, &got)
	if got.Region != "us" || got.Name != "app" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestSquash(t *testing.T) {
	type database struct {
		Host string `mapstructure:"db_host"`
		Port int    `mapstructure:"db_port"`
	}
	type config struct {
		Database database `mapstructure:",squash"`
		Name     string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"db_host": "db.internal",
		"db_port": 5432,
		"name":    "app",
	}, &got)
	if got.Database.Host != "db.internal" || got.Database.Port != 5432 || got.Name != "app" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestRemain(t *testing.T) {
	type config struct {
		Name  string
		Extra map[string]any `mapstructure:",remain"`
	}

	decoder := decode.New(decode.Config{Strict: true})
	var got config
	decodeInto(t, decoder, map[string]any{
		"name":  "app",
		"other": 7,
		"more":  []any{"a", "b"},
	}, &got)

	if got.Name != "app" {
		t.Fatalf("name = %q", got.Name)
	}
	if len(got.Extra) != 2 || got.Extra["other"] != int64(7) {
		t.Fatalf("extra = %#v", got.Extra)
	}
	list, ok := got.Extra["more"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" {
		t.Fatalf("extra[more] = %#v", got.Extra["more"])
	}

	type stringRemain struct {
		Name string
		Rest map[string]string `mapstructure:",remain"`
	}
	var stringsGot stringRemain
	decodeInto(t, decode.New(decode.Config{WeaklyTyped: true}), map[string]any{
		"name": "app",
		"a":    1,
	}, &stringsGot)
	if stringsGot.Rest["a"] != "1" {
		t.Fatalf("rest = %#v", stringsGot.Rest)
	}
}

func TestPointers(t *testing.T) {
	type nested struct {
		Value string
	}
	type config struct {
		Flag   *bool
		Number *int
		Label  **string
		Nested *nested
		Absent *string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"flag":   true,
		"number": 42,
		"label":  "deep",
		"nested": map[string]any{"value": "inner"},
	}, &got)

	if got.Flag == nil || *got.Flag != true {
		t.Fatalf("flag = %v", got.Flag)
	}
	if got.Number == nil || *got.Number != 42 {
		t.Fatalf("number = %v", got.Number)
	}
	if got.Label == nil || *got.Label == nil || **got.Label != "deep" {
		t.Fatalf("label = %v", got.Label)
	}
	if got.Nested == nil || got.Nested.Value != "inner" {
		t.Fatalf("nested = %+v", got.Nested)
	}
	if got.Absent != nil {
		t.Fatalf("absent = %v", got.Absent)
	}
}

func TestSlicesAndArrays(t *testing.T) {
	type config struct {
		Names    []string
		Ports    []int
		Times    []time.Duration
		Pair     [2]string
		Bytes    []byte
		Nested   []struct{ Value int }
		Pointers []*int
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"names":    []any{"a", "b"},
		"ports":    []any{1, 2, 3},
		"times":    []any{"1s", "2m"},
		"pair":     []any{"left", "right"},
		"bytes":    []any{104, 105},
		"nested":   []any{map[string]any{"value": 5}},
		"pointers": []any{1, 2},
	}, &got)

	if !reflect.DeepEqual(got.Names, []string{"a", "b"}) {
		t.Fatalf("names = %#v", got.Names)
	}
	if !reflect.DeepEqual(got.Ports, []int{1, 2, 3}) {
		t.Fatalf("ports = %#v", got.Ports)
	}
	if !reflect.DeepEqual(got.Times, []time.Duration{time.Second, 2 * time.Minute}) {
		t.Fatalf("times = %#v", got.Times)
	}
	if got.Pair != [2]string{"left", "right"} {
		t.Fatalf("pair = %#v", got.Pair)
	}
	if string(got.Bytes) != "hi" {
		t.Fatalf("bytes = %q", got.Bytes)
	}
	if len(got.Nested) != 1 || got.Nested[0].Value != 5 {
		t.Fatalf("nested = %#v", got.Nested)
	}
	if len(got.Pointers) != 2 || *got.Pointers[1] != 2 {
		t.Fatalf("pointers = %#v", got.Pointers)
	}
}

func TestArrayLengthMismatch(t *testing.T) {
	var got struct {
		Pair [2]string
	}
	err := decodeErr(decode.New(decode.Config{}), map[string]any{
		"pair": []any{"only"},
	}, &got)
	if err == nil || !strings.Contains(err.Error(), "config.pair") {
		t.Fatalf("error = %v", err)
	}
}

func TestMaps(t *testing.T) {
	type entry struct {
		Weight int
	}
	type config struct {
		Labels  map[string]string
		Weights map[string]int
		Entries map[string]entry
		Generic map[string]any
		Lists   map[string][]string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"labels":  map[string]any{"env": "prod"},
		"weights": map[string]any{"a": 1, "b": 2},
		"entries": map[string]any{"first": map[string]any{"weight": 3}},
		"generic": map[string]any{"n": 4, "s": "x"},
		"lists":   map[string]any{"k": []any{"v1", "v2"}},
	}, &got)

	if got.Labels["env"] != "prod" {
		t.Fatalf("labels = %#v", got.Labels)
	}
	if got.Weights["b"] != 2 {
		t.Fatalf("weights = %#v", got.Weights)
	}
	if got.Entries["first"].Weight != 3 {
		t.Fatalf("entries = %#v", got.Entries)
	}
	if got.Generic["n"] != int64(4) || got.Generic["s"] != "x" {
		t.Fatalf("generic = %#v", got.Generic)
	}
	if !reflect.DeepEqual(got.Lists["k"], []string{"v1", "v2"}) {
		t.Fatalf("lists = %#v", got.Lists)
	}
}

func TestMapEntriesAreIndependent(t *testing.T) {
	type tenant struct {
		Name string
		Port int
		Tags []string
	}
	var got map[string]tenant
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"first": map[string]any{
			"name": "one",
			"port": 1,
			"tags": []any{"a", "b"},
		},
		"second": map[string]any{
			"name": "two",
		},
	}, &got)

	if len(got) != 2 {
		t.Fatalf("decoded %#v", got)
	}
	first, ok := got["first"]
	if !ok || first.Name != "one" || first.Port != 1 || !reflect.DeepEqual(first.Tags, []string{"a", "b"}) {
		t.Fatalf("first = %#v", first)
	}
	second, ok := got["second"]
	if !ok || second.Name != "two" || second.Port != 0 || second.Tags != nil {
		t.Fatalf("second = %#v", second)
	}
	if len(first.Tags) > 0 && len(second.Tags) > 0 && &first.Tags[0] == &second.Tags[0] {
		t.Fatal("map entries share a slice")
	}
}

func TestMapLargeDecode(t *testing.T) {
	type tenant struct {
		TenantCode string `mapstructure:"tenantCode"`
		Domain     string
	}
	values := make(map[string]any, 1001)
	for i := 1; i <= 1001; i++ {
		values[fmt.Sprintf("tenant%d", i)] = map[string]any{
			"tenantCode": fmt.Sprintf("tenant%d", i),
			"domain":     fmt.Sprintf("tenant%d.example", i),
		}
	}

	var got map[string]tenant
	decodeInto(t, decode.New(decode.Config{}), values, &got)
	if len(got) != 1001 {
		t.Fatalf("len = %d", len(got))
	}
	if got["tenant1001"].Domain != "tenant1001.example" {
		t.Fatalf("tenant1001 = %#v", got["tenant1001"])
	}
}

func TestMapWithNonStringKeysRejected(t *testing.T) {
	var got struct {
		Values map[int]string
	}
	err := decodeErr(decode.New(decode.Config{}), map[string]any{
		"values": map[string]any{"1": "one"},
	}, &got)
	if err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("error = %v", err)
	}
}

func TestAny(t *testing.T) {
	type config struct {
		Anything any
		Object   any
		List     any
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"anything": "text",
		"object":   map[string]any{"k": 1},
		"list":     []any{1, "two"},
	}, &got)

	if got.Anything != "text" {
		t.Fatalf("anything = %#v", got.Anything)
	}
	object, ok := got.Object.(map[string]any)
	if !ok || object["k"] != int64(1) {
		t.Fatalf("object = %#v", got.Object)
	}
	list, ok := got.List.([]any)
	if !ok || len(list) != 2 || list[1] != "two" {
		t.Fatalf("list = %#v", got.List)
	}
}

func TestWeakConversions(t *testing.T) {
	type config struct {
		Flag     bool
		Number   int
		Unsigned uint
		Float    float64
		Text     string
		Duration time.Duration
		At       time.Time
		Endpoint url.URL
		Numbers  []int
		Pair     [2]int
		Single   []string
	}

	var got config
	decodeInto(t, decode.New(decode.Config{WeaklyTyped: true}), map[string]any{
		"flag":     "true",
		"number":   "17",
		"unsigned": "18",
		"float":    "2.5",
		"text":     99,
		"duration": "1m30s",
		"at":       "2024-05-06",
		"endpoint": "https://example.test/path",
		"numbers":  "1, 2,3",
		"pair":     "8,9",
		"single":   "only",
	}, &got)

	if !got.Flag || got.Number != 17 || got.Unsigned != 18 || got.Float != 2.5 {
		t.Fatalf("scalars = %+v", got)
	}
	if got.Text != "99" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.Duration != 90*time.Second {
		t.Fatalf("duration = %v", got.Duration)
	}
	if got.At.Year() != 2024 || got.At.Month() != time.May || got.At.Day() != 6 {
		t.Fatalf("at = %v", got.At)
	}
	if got.Endpoint.Host != "example.test" {
		t.Fatalf("endpoint = %+v", got.Endpoint)
	}
	if !reflect.DeepEqual(got.Numbers, []int{1, 2, 3}) {
		t.Fatalf("numbers = %#v", got.Numbers)
	}
	if got.Pair != [2]int{8, 9} {
		t.Fatalf("pair = %#v", got.Pair)
	}
	if !reflect.DeepEqual(got.Single, []string{"only"}) {
		t.Fatalf("single = %#v", got.Single)
	}
}

func TestWeakFloatToInt(t *testing.T) {
	decoder := decode.New(decode.Config{WeaklyTyped: true})
	var got struct {
		Number int
	}
	decodeInto(t, decoder, map[string]any{"number": 3.0}, &got)
	if got.Number != 3 {
		t.Fatalf("number = %d", got.Number)
	}

	err := decodeErr(decoder, map[string]any{"number": 3.5}, &got)
	if err == nil || !strings.Contains(err.Error(), "precision") {
		t.Fatalf("error = %v", err)
	}
}

func TestWeakDisabledErrors(t *testing.T) {
	decoder := decode.New(decode.Config{})

	tests := []struct {
		name   string
		values map[string]any
		dst    any
	}{
		{"string to int", map[string]any{"value": "12"}, &struct{ Value int }{}},
		{"string to bool", map[string]any{"value": "true"}, &struct{ Value bool }{}},
		{"int to string", map[string]any{"value": 12}, &struct{ Value string }{}},
		{"string to slice", map[string]any{"value": "a,b"}, &struct{ Value []string }{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := decodeErr(decoder, test.values, test.dst)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "weakly typed") {
				t.Fatalf("error %v does not mention weak typing", err)
			}
		})
	}
}

func TestTimeAndDurationForms(t *testing.T) {
	type config struct {
		RFC3339  time.Time
		Date     time.Time
		Unix     time.Time
		Duration time.Duration
		Nanos    time.Duration
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"rfc3339":  "2024-05-06T07:08:09Z",
		"date":     "2024-05-06",
		"unix":     1714979289,
		"duration": "250ms",
		"nanos":    1500,
	}, &got)

	if got.RFC3339.UTC().Format(time.RFC3339) != "2024-05-06T07:08:09Z" {
		t.Fatalf("rfc3339 = %v", got.RFC3339)
	}
	if got.Date.Year() != 2024 || got.Date.Month() != time.May || got.Date.Day() != 6 {
		t.Fatalf("date = %v", got.Date)
	}
	if !got.Unix.Equal(time.Unix(1714979289, 0).UTC()) {
		t.Fatalf("unix = %v", got.Unix)
	}
	if got.Duration != 250*time.Millisecond {
		t.Fatalf("duration = %v", got.Duration)
	}
	if got.Nanos != 1500*time.Nanosecond {
		t.Fatalf("nanos = %v", got.Nanos)
	}
}

// upper is a named string type with a validating text unmarshaller.
type upper string

func (u *upper) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		return errors.New("empty value")
	}
	*u = upper(strings.ToUpper(string(text)))
	return nil
}

// coordinates is a struct type decoded through its text unmarshaller.
type coordinates struct {
	Latitude  int
	Longitude int
}

func (c *coordinates) UnmarshalText(text []byte) error {
	parts := strings.Split(string(text), ";")
	if len(parts) != 2 {
		return fmt.Errorf("expected two parts, got %d", len(parts))
	}
	if _, err := fmt.Sscanf(strings.Join(parts, " "), "%d %d", &c.Latitude, &c.Longitude); err != nil {
		return err
	}
	return nil
}

func TestTextUnmarshaler(t *testing.T) {
	type config struct {
		Name  upper
		Point coordinates
	}

	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"name":  "billing",
		"point": "10;20",
	}, &got)

	if got.Name != upper("BILLING") {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Point.Latitude != 10 || got.Point.Longitude != 20 {
		t.Fatalf("point = %+v", got.Point)
	}

	err := decodeErr(decode.New(decode.Config{}), map[string]any{
		"name":  "ok",
		"point": "broken",
	}, &got)
	if err == nil || !strings.Contains(err.Error(), "config.point") {
		t.Fatalf("error = %v", err)
	}
}

// level is decoded through a custom hook.
type level int

func TestHookConversion(t *testing.T) {
	toLevel := func(from, to reflect.Type, data any) (any, error) {
		if to != reflect.TypeOf(level(0)) || from.Kind() != reflect.String {
			return data, nil
		}
		switch data {
		case "debug":
			return level(1), nil
		case "info":
			return level(2), nil
		default:
			return nil, fmt.Errorf("unknown level %q", data)
		}
	}

	type config struct {
		Level level
	}

	decoder := decode.New(decode.Config{Hooks: []hook.Func{toLevel}})
	var got config
	decodeInto(t, decoder, map[string]any{"level": "debug"}, &got)
	if got.Level != 1 {
		t.Fatalf("level = %d", got.Level)
	}

	err := decodeErr(decoder, map[string]any{"level": "loud"}, &config{})
	if err == nil || !strings.Contains(err.Error(), "config.level") || !strings.Contains(err.Error(), "unknown level") {
		t.Fatalf("error = %v", err)
	}
}

func TestHookNilAssignsZero(t *testing.T) {
	clearHook := func(from, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeOf(0) && from.Kind() == reflect.String {
			return nil, nil
		}
		return data, nil
	}
	var got struct{ Number int }
	decodeInto(t, decode.New(decode.Config{Hooks: []hook.Func{clearHook}}), map[string]any{"number": "5"}, &got)
	if got.Number != 0 {
		t.Fatalf("number = %d", got.Number)
	}
}

func TestHookCustomTimeLayout(t *testing.T) {
	type config struct {
		At time.Time
	}
	decoder := decode.New(decode.Config{Hooks: []hook.Func{hook.StringToTime("2006/01/02")}})
	var got config
	decodeInto(t, decoder, map[string]any{"at": "2023/11/12"}, &got)
	if got.At.Year() != 2023 || got.At.Month() != time.November || got.At.Day() != 12 {
		t.Fatalf("at = %v", got.At)
	}
}

func TestHookStringToSlice(t *testing.T) {
	type config struct {
		Numbers []int
	}
	decoder := decode.New(decode.Config{Hooks: []hook.Func{hook.StringToSlice(",")}})
	var got config
	decodeInto(t, decoder, map[string]any{"numbers": "4, 5,6"}, &got)
	if !reflect.DeepEqual(got.Numbers, []int{4, 5, 6}) {
		t.Fatalf("numbers = %#v", got.Numbers)
	}
}

func TestHookComposeDefault(t *testing.T) {
	type config struct {
		Duration time.Duration
		At       time.Time
		Endpoint url.URL
	}
	decoder := decode.New(decode.Config{Hooks: []hook.Func{hook.Default}})
	var got config
	decodeInto(t, decoder, map[string]any{
		"duration": "2s",
		"at":       "2022-03-04T05:06:07Z",
		"endpoint": "https://example.test/",
	}, &got)
	if got.Duration != 2*time.Second {
		t.Fatalf("duration = %v", got.Duration)
	}
	if got.At.Year() != 2022 {
		t.Fatalf("at = %v", got.At)
	}
	if got.Endpoint.Host != "example.test" {
		t.Fatalf("endpoint = %+v", got.Endpoint)
	}
}

func TestHookNotAppliedToAny(t *testing.T) {
	toTime := func(from, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeOf(time.Time{}) && from.Kind() == reflect.String {
			parsed, err := time.Parse(time.RFC3339, data.(string))
			if err != nil {
				return nil, err
			}
			return parsed, nil
		}
		return data, nil
	}
	type config struct {
		Anything any
	}
	decoder := decode.New(decode.Config{Hooks: []hook.Func{toTime}})
	var got config
	decodeInto(t, decoder, map[string]any{"anything": "2020-01-02T03:04:05Z"}, &got)
	if _, ok := got.Anything.(string); !ok {
		t.Fatalf("anything = %#v", got.Anything)
	}
}

func TestStrictUnknownKeys(t *testing.T) {
	type database struct {
		Host string
	}
	type config struct {
		Name     string
		Database database
	}

	decoder := decode.New(decode.Config{Strict: true})
	err := decodeErr(decoder, map[string]any{
		"name":     "app",
		"database": map[string]any{"host": "db", "port": 5432},
	}, &config{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "config.database.port") {
		t.Fatalf("error %v does not contain the configuration path", err)
	}

	var got config
	if err := decoder.Decode(doc(map[string]any{
		"name":     "app",
		"database": map[string]any{"host": "db"},
	}), &got); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Database.Host != "db" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestNonStrictIgnoresUnknownKeys(t *testing.T) {
	var got struct{ Name string }
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"name":    "app",
		"unknown": 1,
	}, &got)
	if got.Name != "app" {
		t.Fatalf("decoded %+v", got)
	}
}

func TestStrictRemainAcceptsUnknownKeys(t *testing.T) {
	type config struct {
		Name string
		Rest map[string]any `mapstructure:",remain"`
	}
	var got config
	decodeInto(t, decode.New(decode.Config{Strict: true}), map[string]any{
		"name": "app",
		"x":    1,
	}, &got)
	if len(got.Rest) != 1 {
		t.Fatalf("rest = %#v", got.Rest)
	}
}

func TestNullValues(t *testing.T) {
	type config struct {
		Name   string
		Number int
		Flag   *bool
		List   []string
	}
	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"name":   nil,
		"number": nil,
		"flag":   nil,
		"list":   nil,
	}, &got)
	if got.Name != "" || got.Number != 0 || got.Flag != nil || got.List != nil {
		t.Fatalf("decoded %+v", got)
	}
}

func TestErrorPaths(t *testing.T) {
	type nested struct {
		Count int
	}
	type config struct {
		Nested   nested
		Numbers  []int
		Registry map[string]nested
	}
	decoder := decode.New(decode.Config{})

	err := decodeErr(decoder, map[string]any{
		"nested": map[string]any{"count": "many"},
	}, &config{})
	if err == nil || !strings.Contains(err.Error(), "config.nested.count") {
		t.Fatalf("error = %v", err)
	}

	err = decodeErr(decoder, map[string]any{
		"numbers": []any{1, "two"},
	}, &config{})
	if err == nil || !strings.Contains(err.Error(), "config.numbers[1]") {
		t.Fatalf("error = %v", err)
	}

	err = decodeErr(decoder, map[string]any{
		"registry": map[string]any{"first": map[string]any{"count": "many"}},
	}, &config{})
	if err == nil || !strings.Contains(err.Error(), "config.registry[first].count") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeValidation(t *testing.T) {
	decoder := decode.New(decode.Config{})
	src := doc(map[string]any{"name": "app"})

	if err := decoder.Decode(src, nil); err == nil {
		t.Fatal("expected an error for a nil destination")
	}
	var notPointer struct{ Name string }
	if err := decoder.Decode(src, notPointer); err == nil {
		t.Fatal("expected an error for a non-pointer destination")
	}
	var nilPointer *struct{ Name string }
	if err := decoder.Decode(src, nilPointer); err == nil {
		t.Fatal("expected an error for a nil pointer destination")
	}
	var target struct{ Name string }
	if err := decoder.Decode(nil, &target); err != nil {
		t.Fatalf("nil source error = %v", err)
	}
	if target.Name != "" {
		t.Fatalf("nil source decoded %+v", target)
	}
}

func TestNilSourcePointerDecodesNull(t *testing.T) {
	decoder := decode.New(decode.Config{})
	var target struct {
		Name string
	}
	if err := decoder.Decode(nil, &target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if target.Name != "" {
		t.Fatalf("decoded %+v", target)
	}
}

func TestNamedTypes(t *testing.T) {
	type host string
	type port int
	type config struct {
		Host host
		Port port
	}
	var got config
	decodeInto(t, decode.New(decode.Config{}), map[string]any{
		"host": "db",
		"port": 5432,
	}, &got)
	if got.Host != "db" || got.Port != 5432 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestPlanErrors(t *testing.T) {
	t.Run("squash on non struct", func(t *testing.T) {
		type bad struct {
			Name string `mapstructure:",squash"`
		}
		var got bad
		err := decodeErr(decode.New(decode.Config{}), map[string]any{"name": "x"}, &got)
		if err == nil || !strings.Contains(err.Error(), "squash") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("remain on non map", func(t *testing.T) {
		type bad struct {
			Rest string `mapstructure:",remain"`
		}
		var got bad
		err := decodeErr(decode.New(decode.Config{}), map[string]any{"x": "y"}, &got)
		if err == nil || !strings.Contains(err.Error(), "remain") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("multiple remain", func(t *testing.T) {
		type bad struct {
			First  map[string]any `mapstructure:",remain"`
			Second map[string]any `mapstructure:",remain"`
		}
		var got bad
		err := decodeErr(decode.New(decode.Config{}), map[string]any{"x": "y"}, &got)
		if err == nil || !strings.Contains(err.Error(), "multiple remain") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("recursive flattening", func(t *testing.T) {
		type recursive struct {
			Next *recursive `mapstructure:",squash"`
		}
		var got recursive
		err := decodeErr(decode.New(decode.Config{}), map[string]any{"x": "y"}, &got)
		if err == nil || !strings.Contains(err.Error(), "recursive") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestConcurrentDecode(t *testing.T) {
	type database struct {
		Host string
		Port int
	}
	type config struct {
		Name     string
		Debug    bool
		Timeout  time.Duration
		Tags     []string
		Database database
		Extra    map[string]int
	}

	decoder := decode.New(decode.Config{WeaklyTyped: true})
	src := doc(map[string]any{
		"name":    "app",
		"debug":   "true",
		"timeout": "3s",
		"tags":    []any{"a", "b"},
		"database": map[string]any{
			"host": "db",
			"port": "5432",
		},
		"extra": map[string]any{"one": "1"},
	})

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				var got config
				if err := decoder.Decode(src, &got); err != nil {
					errs <- err
					return
				}
				if got.Name != "app" || !got.Debug || got.Database.Port != 5432 || got.Extra["one"] != 1 {
					errs <- fmt.Errorf("unexpected decode result %+v", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestParallelSubtestsShareDecoder(t *testing.T) {
	type config struct {
		Name   string
		Values []int
	}
	decoder := decode.New(decode.Config{WeaklyTyped: true})

	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			t.Parallel()
			var got config
			values := []any{i, i + 1}
			if err := decoder.Decode(doc(map[string]any{
				"name":   fmt.Sprintf("n%d", i),
				"values": values,
			}), &got); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got.Name != fmt.Sprintf("n%d", i) || len(got.Values) != 2 || got.Values[0] != i {
				t.Fatalf("decoded %+v", got)
			}
		})
	}
}

func TestDecodeTwiceKeepsAbsentFields(t *testing.T) {
	type config struct {
		Name string
		Port int
	}
	decoder := decode.New(decode.Config{})
	got := config{Name: "first", Port: 1}
	decodeInto(t, decoder, map[string]any{"name": "second"}, &got)
	if got.Name != "second" || got.Port != 1 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestUnsupportedDestination(t *testing.T) {
	var got struct {
		Channel chan int
	}
	err := decodeErr(decode.New(decode.Config{}), map[string]any{"channel": "x"}, &got)
	if err == nil || !strings.Contains(err.Error(), "config.channel") {
		t.Fatalf("error = %v", err)
	}
}
