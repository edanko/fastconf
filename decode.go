package fastconf

import (
	"strings"

	"github.com/edanko/fastconf/hook"
	"github.com/edanko/fastconf/internal/value"
)

// DecoderConfig configures struct decoding.
type DecoderConfig struct {
	// WeaklyTypedInput enables conversions such as string to int, number to
	// string or comma separated string to slice. Enabled by default.
	WeaklyTypedInput bool

	// Strict makes decoding fail on configuration keys that do not match a
	// struct field.
	Strict bool

	// TagName is the struct tag used for field names. The default is
	// "mapstructure".
	TagName string

	// DecodeHooks are applied before built-in conversions.
	DecodeHooks []hook.Func
}

func defaultDecoderConfig() DecoderConfig {
	return DecoderConfig{
		WeaklyTypedInput: true,
		TagName:          "mapstructure",
	}
}

func splitPath(configPath string) []string {
	return strings.Split(configPath, ".")
}

func valueOfObject(object *value.Object) value.Value {
	return value.ObjectValue(object)
}

func valueOfMap(object map[string]any) *value.Value {
	converted := value.From(object)
	return &converted
}
