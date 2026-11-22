// Package hook provides decode hooks: functions that convert a configuration
// value from one Go type to another before the decoder assigns it.
//
// A hook is called with the source type, the destination type and the source
// value. It returns the value to assign. Returning a value whose type is the
// source type means the hook declined the conversion, so the decoder falls back
// to its built-in rules. Returning a value of the destination type ends the
// conversion; returning any other type lets the decoder continue with the
// returned value, which is how one hook can feed the next.
//
// Hooks are only consulted when a value cannot be assigned to the destination
// directly, so ordinary scalar and structured decoding never pays for them.
package hook

import (
	"encoding"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Func converts data of the from type into the to type.
//
// A hook must change the type of the value to take effect: returning data of
// the from type signals that the hook declined the conversion. Returning a nil
// value with a nil error assigns the zero value to the destination.
type Func func(from, to reflect.Type, data any) (any, error)

var (
	durationType        = reflect.TypeOf(time.Duration(0))
	timeType            = reflect.TypeOf(time.Time{})
	urlType             = reflect.TypeOf(url.URL{})
	urlPointerType      = reflect.TypeOf((*url.URL)(nil))
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// defaultTimeLayouts are the layouts tried by StringToTime when no explicit
// layout is supplied.
var defaultTimeLayouts = []string{time.RFC3339, "2006-01-02"}

// Compose combines funcs into a single hook applied in order. A hook that
// declines the conversion passes the value to the next one. Composition stops
// as soon as a hook produces the destination type or a nil value. Nil hooks are
// ignored. Compose without any non-nil hook is the identity hook.
func Compose(funcs ...Func) Func {
	chain := make([]Func, 0, len(funcs))
	for _, fn := range funcs {
		if fn != nil {
			chain = append(chain, fn)
		}
	}
	if len(chain) == 0 {
		return func(_ /* from */, _ /* to */ reflect.Type, data any) (any, error) {
			return data, nil
		}
	}

	return func(from, to reflect.Type, data any) (any, error) {
		for _, fn := range chain {
			next, err := fn(from, to, data)
			if err != nil {
				return nil, err
			}
			if next == nil {
				return nil, nil
			}
			nextType := reflect.TypeOf(next)
			if nextType == from {
				// The hook declined; leave the value untouched.
				continue
			}
			data, from = next, nextType
			if from == to {
				break
			}
		}
		return data, nil
	}
}

// StringToTimeDuration converts duration strings such as "1h30m" or "250ms"
// into time.Duration.
func StringToTimeDuration() Func {
	return func(from, to reflect.Type, data any) (any, error) {
		if to != durationType || from == nil || from.Kind() != reflect.String {
			return data, nil
		}
		text := reflect.ValueOf(data).String()
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return nil, fmt.Errorf("hook: cannot parse %q as time.Duration: %w", text, err)
		}
		return parsed, nil
	}
}

// StringToTime converts time strings into time.Time. The given layouts are
// tried in order; with no layouts, RFC 3339 and "2006-01-02" are used.
func StringToTime(layouts ...string) Func {
	accepted := layouts
	if len(accepted) == 0 {
		accepted = defaultTimeLayouts
	} else {
		accepted = append([]string(nil), layouts...)
	}

	return func(from, to reflect.Type, data any) (any, error) {
		if to != timeType || from == nil || from.Kind() != reflect.String {
			return data, nil
		}
		text := reflect.ValueOf(data).String()
		var lastErr error
		for _, layout := range accepted {
			parsed, err := time.Parse(layout, text)
			if err == nil {
				return parsed, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("hook: cannot parse %q as time.Time using layouts %s: %w",
			text, strings.Join(accepted, ", "), lastErr)
	}
}

// StringToURL converts URL strings into url.URL values or pointers.
func StringToURL() Func {
	return func(from, to reflect.Type, data any) (any, error) {
		if (to != urlType && to != urlPointerType) || from == nil || from.Kind() != reflect.String {
			return data, nil
		}
		text := reflect.ValueOf(data).String()
		parsed, err := url.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("hook: cannot parse %q as url.URL: %w", text, err)
		}
		if to == urlPointerType {
			return parsed, nil
		}
		return *parsed, nil
	}
}

// StringToSlice splits strings into slices or arrays. With an empty separator,
// "," is used. Surrounding spaces of every element are trimmed. Element types
// may be strings, booleans, integers, unsigned integers, floats, time.Duration,
// time.Time or types implementing encoding.TextUnmarshaler.
func StringToSlice(separator string) Func {
	if separator == "" {
		separator = ","
	}

	return func(from, to reflect.Type, data any) (any, error) {
		if to == nil || (to.Kind() != reflect.Slice && to.Kind() != reflect.Array) {
			return data, nil
		}
		if from == nil || from.Kind() != reflect.String {
			return data, nil
		}
		text := reflect.ValueOf(data).String()
		var parts []string
		if text != "" {
			parts = strings.Split(text, separator)
		}
		elemType := to.Elem()
		length := len(parts)

		if to.Kind() == reflect.Array {
			if to.Len() != length {
				return nil, fmt.Errorf("hook: cannot split %q into %s: got %d elements, want %d",
					text, to, length, to.Len())
			}
			array := reflect.New(to).Elem()
			for i := 0; i < length; i++ {
				elem, err := convertPart(parts[i], elemType)
				if err != nil {
					return nil, err
				}
				array.Index(i).Set(elem)
			}
			return array.Interface(), nil
		}

		slice := reflect.MakeSlice(to, length, length)
		for i := 0; i < length; i++ {
			elem, err := convertPart(parts[i], elemType)
			if err != nil {
				return nil, err
			}
			slice.Index(i).Set(elem)
		}
		return slice.Interface(), nil
	}
}

// Default is the hook used by the loader when no explicit hooks are configured.
// It converts duration, time and URL strings.
var Default Func = Compose(
	StringToTimeDuration(),
	StringToTime(),
	StringToURL(),
)

// convertPart converts one element of a split string into elemType.
func convertPart(part string, elemType reflect.Type) (reflect.Value, error) {
	text := strings.TrimSpace(part)
	out := reflect.New(elemType).Elem()

	switch elemType {
	case durationType:
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot parse %q as time.Duration: %w", part, err)
		}
		out.SetInt(int64(parsed))
		return out, nil
	case timeType:
		parsed, err := parseTime(text)
		if err != nil {
			return reflect.Value{}, err
		}
		out.Set(reflect.ValueOf(parsed))
		return out, nil
	}

	switch elemType.Kind() {
	case reflect.String:
		out.SetString(text)
		return out, nil
	case reflect.Bool:
		value, err := strconv.ParseBool(text)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot convert %q to %s: %w", part, elemType, err)
		}
		out.SetBool(value)
		return out, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, err := strconv.ParseInt(text, 10, elemType.Bits())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot convert %q to %s: %w", part, elemType, err)
		}
		out.SetInt(number)
		return out, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		number, err := strconv.ParseUint(text, 10, elemType.Bits())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot convert %q to %s: %w", part, elemType, err)
		}
		out.SetUint(number)
		return out, nil
	case reflect.Float32, reflect.Float64:
		number, err := strconv.ParseFloat(text, elemType.Bits())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot convert %q to %s: %w", part, elemType, err)
		}
		out.SetFloat(number)
		return out, nil
	case reflect.Interface:
		if elemType.NumMethod() == 0 {
			out.Set(reflect.ValueOf(text))
			return out, nil
		}
	}

	if reflect.PointerTo(elemType).Implements(textUnmarshalerType) {
		pointer := reflect.New(elemType)
		if err := pointer.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
			return reflect.Value{}, fmt.Errorf("hook: cannot parse %q as %s: %w", part, elemType, err)
		}
		out.Set(pointer.Elem())
		return out, nil
	}
	return reflect.Value{}, fmt.Errorf("hook: cannot convert %q to %s", part, elemType)
}

// parseTime parses text using the default time layouts.
func parseTime(text string) (time.Time, error) {
	var lastErr error
	for _, layout := range defaultTimeLayouts {
		parsed, err := time.Parse(layout, strings.TrimSpace(text))
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("hook: cannot parse %q as time.Time: %w", text, lastErr)
}
