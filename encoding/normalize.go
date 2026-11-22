package encoding

import (
	"errors"
	"fmt"
	"reflect"
)

// normalize converts decoded values into the canonical document
// representation: objects become map[string]any, lists become []any and every
// other value is returned unchanged.
//
// The conversion happens in place where possible, because decoders own the
// maps and slices they return.
func normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalize(child)
		}
		return typed
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, child := range typed {
			converted[keyString(key)] = normalize(child)
		}
		return converted
	case []any:
		for index, child := range typed {
			typed[index] = normalize(child)
		}
		return typed
	case []map[string]any:
		converted := make([]any, len(typed))
		for index, child := range typed {
			converted[index] = normalize(child)
		}
		return converted
	case []byte:
		// Binary blobs are scalar values, not lists of numbers.
		return typed
	default:
		return normalizeOther(value)
	}
}

// normalizeOther converts container values that a custom decoder may produce
// with concrete element or key types. It only inspects values that are not one
// of the common container shapes, so scalars keep their exact type.
func normalizeOther(value any) any {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Map:
		converted := make(map[string]any, reflected.Len())
		iter := reflected.MapRange()
		for iter.Next() {
			converted[keyString(iter.Key().Interface())] = normalize(iter.Value().Interface())
		}
		return converted
	case reflect.Slice, reflect.Array:
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			return value
		}
		converted := make([]any, reflected.Len())
		for index := range reflected.Len() {
			converted[index] = normalize(reflected.Index(index).Interface())
		}
		return converted
	default:
		return value
	}
}

// objectRoot validates that a decoded value is a document object and converts
// its keys to strings. Nested values are normalized later, by Decode.
func objectRoot(value any) (map[string]any, error) {
	switch typed := value.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return typed, nil
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, child := range typed {
			converted[keyString(key)] = child
		}
		return converted, nil
	case []any:
		return nil, errors.New("document root is a list, want an object")
	default:
		return nil, errors.New("document root is a scalar, want an object")
	}
}

// keyString renders an object key as a string. Keys of map[any]any documents
// are strings in every supported format; other scalar keys are stringified so
// that unusual documents are still accepted.
func keyString(key any) string {
	if text, ok := key.(string); ok {
		return text
	}
	return fmt.Sprint(key)
}
