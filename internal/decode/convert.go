package decode

import (
	"encoding"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/edanko/fastconf/internal/value"
)

var (
	boolType            = reflect.TypeOf(false)
	int64Type           = reflect.TypeFor[int64]()
	float64Type         = reflect.TypeFor[float64]()
	stringType          = reflect.TypeFor[string]()
	timeType            = reflect.TypeFor[time.Time]()
	durationType        = reflect.TypeFor[time.Duration]()
	urlType             = reflect.TypeFor[url.URL]()
	anySliceType        = reflect.TypeFor[[]any]()
	stringAnyMapType    = reflect.TypeFor[map[string]any]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// timeLayouts are the string layouts accepted for time.Time values.
var timeLayouts = []string{time.RFC3339, "2006-01-02"}

// assign handles values that are assignable to the destination without weak
// typing: booleans, strings, numbers and the usual numeric widening. It
// reports whether it consumed the value.
func (d *Decoder) assign(v value.Value, dst reflect.Value, path *pathStack) (bool, error) {
	kind := v.Kind()
	switch dst.Kind() {
	case reflect.Bool:
		if kind == value.KindBool {
			dst.SetBool(v.Bool())
			return true, nil
		}
	case reflect.String:
		if kind == value.KindString {
			dst.SetString(v.String())
			return true, nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if kind == value.KindInt {
			number := v.Int()
			if dst.OverflowInt(number) {
				return true, path.errorf("value %d overflows %s", number, dst.Type())
			}
			dst.SetInt(number)
			return true, nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if kind == value.KindInt {
			number := v.Int()
			if number < 0 {
				return true, path.errorf("cannot decode negative value %d into %s", number, dst.Type())
			}
			if dst.OverflowUint(uint64(number)) {
				return true, path.errorf("value %d overflows %s", number, dst.Type())
			}
			dst.SetUint(uint64(number))
			return true, nil
		}
	case reflect.Float32, reflect.Float64:
		if kind == value.KindInt || kind == value.KindFloat {
			dst.SetFloat(v.Float())
			return true, nil
		}
	}
	return false, nil
}

// weakValue converts scalar values in weakly typed mode. It reports whether it
// consumed the value.
func (d *Decoder) weakValue(v value.Value, dst reflect.Value, path *pathStack) (bool, error) {
	kind := v.Kind()
	switch dst.Kind() {
	case reflect.String:
		switch kind {
		case value.KindBool:
			dst.SetString(strconv.FormatBool(v.Bool()))
			return true, nil
		case value.KindInt:
			dst.SetString(strconv.FormatInt(v.Int(), 10))
			return true, nil
		case value.KindFloat:
			dst.SetString(strconv.FormatFloat(v.Float(), 'g', -1, 64))
			return true, nil
		}
	case reflect.Bool:
		if kind == value.KindString {
			parsed, err := strconv.ParseBool(strings.TrimSpace(v.String()))
			if err != nil {
				return true, path.errorf("cannot decode %q as %s", v.String(), dst.Type())
			}
			dst.SetBool(parsed)
			return true, nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch kind {
		case value.KindString:
			parsed, err := strconv.ParseInt(strings.TrimSpace(v.String()), 10, dst.Type().Bits())
			if err != nil {
				return true, path.errorf("cannot decode %q as %s", v.String(), dst.Type())
			}
			dst.SetInt(parsed)
			return true, nil
		case value.KindFloat:
			number, ok := integralInt(v.Float())
			if !ok || dst.OverflowInt(number) {
				return true, path.errorf("cannot decode %v as %s without losing precision", v.Float(), dst.Type())
			}
			dst.SetInt(number)
			return true, nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		switch kind {
		case value.KindString:
			parsed, err := strconv.ParseUint(strings.TrimSpace(v.String()), 10, dst.Type().Bits())
			if err != nil {
				return true, path.errorf("cannot decode %q as %s", v.String(), dst.Type())
			}
			dst.SetUint(parsed)
			return true, nil
		case value.KindFloat:
			number, ok := integralUint(v.Float())
			if !ok || dst.OverflowUint(number) {
				return true, path.errorf("cannot decode %v as %s without losing precision", v.Float(), dst.Type())
			}
			dst.SetUint(number)
			return true, nil
		}
	case reflect.Float32, reflect.Float64:
		if kind == value.KindString {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v.String()), dst.Type().Bits())
			if err != nil {
				return true, path.errorf("cannot decode %q as %s", v.String(), dst.Type())
			}
			dst.SetFloat(parsed)
			return true, nil
		}
	case reflect.Slice, reflect.Array:
		if v.IsScalar() {
			return true, d.weakList(v, dst, path)
		}
	}
	return false, nil
}

// weakList expands a scalar into a slice or array. Strings are split on commas
// and every element is decoded with the regular rules.
func (d *Decoder) weakList(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()

	var parts []string
	single := false
	switch v.Kind() {
	case value.KindString:
		text := v.String()
		if text != "" {
			parts = strings.Split(text, ",")
		}
	case value.KindBool, value.KindInt, value.KindFloat:
		single = true
	default:
		return d.typeError(path, v, typ)
	}

	length := len(parts)
	if single {
		length = 1
	}
	if typ.Kind() == reflect.Array && typ.Len() != length {
		return path.errorf("cannot decode %s value into %s: got %d elements, want %d",
			v.Kind(), typ, length, typ.Len())
	}

	if typ.Kind() == reflect.Array {
		for i := 0; i < length; i++ {
			path.pushIndex(i)
			err := d.decodeWeakElement(v, parts, single, i, dst.Index(i), path)
			path.pop()
			if err != nil {
				return err
			}
		}
		return nil
	}

	out := reflect.MakeSlice(typ, length, length)
	for i := 0; i < length; i++ {
		path.pushIndex(i)
		err := d.decodeWeakElement(v, parts, single, i, out.Index(i), path)
		path.pop()
		if err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// decodeWeakElement decodes one element of a weakly typed list expansion.
func (d *Decoder) decodeWeakElement(v value.Value, parts []string, single bool, index int, dst reflect.Value, path *pathStack) error {
	if single {
		return d.decode(v, dst, true, path)
	}
	return d.decode(value.String(strings.TrimSpace(parts[index])), dst, true, path)
}

// decodeSpecial handles named types with their own textual representation:
// time.Time, time.Duration and url.URL. It reports whether it consumed the
// value.
func (d *Decoder) decodeSpecial(v value.Value, dst reflect.Value, path *pathStack) (bool, error) {
	typ := dst.Type()
	switch typ {
	case timeType:
		return d.decodeTime(v, dst, path)
	case durationType:
		if v.Kind() != value.KindString {
			return false, nil
		}
		text := strings.TrimSpace(v.String())
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return true, path.errorf("cannot parse %q as time.Duration", v.String())
		}
		dst.SetInt(int64(parsed))
		return true, nil
	case urlType:
		if v.Kind() != value.KindString {
			return false, nil
		}
		text := v.String()
		parsed, err := url.Parse(text)
		if err != nil {
			return true, path.errorf("cannot parse %q as url.URL", text)
		}
		dst.Set(reflect.ValueOf(*parsed))
		return true, nil
	}
	return false, nil
}

// decodeTime converts a value into time.Time from RFC 3339 or date strings,
// unix seconds or a time.Time carried by a generic value.
func (d *Decoder) decodeTime(v value.Value, dst reflect.Value, path *pathStack) (bool, error) {
	switch v.Kind() {
	case value.KindString:
		text := strings.TrimSpace(v.String())
		for _, layout := range timeLayouts {
			if parsed, err := time.Parse(layout, text); err == nil {
				dst.Set(reflect.ValueOf(parsed))
				return true, nil
			}
		}
		return true, path.errorf("cannot parse %q as time.Time, expected RFC 3339 or 2006-01-02", v.String())
	case value.KindInt:
		dst.Set(reflect.ValueOf(time.Unix(v.Int(), 0).UTC()))
		return true, nil
	case value.KindFloat:
		if !d.weak {
			return false, nil
		}
		const maxInt64Exclusive = 9223372036854775808.0
		seconds, nanos := math.Modf(v.Float())
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.IsInf(nanos, 0) ||
			seconds < math.MinInt64 || seconds >= maxInt64Exclusive {
			return true, path.errorf("cannot decode %v as time.Time", v.Float())
		}
		dst.Set(reflect.ValueOf(time.Unix(int64(seconds), int64(nanos*1e9)).UTC()))
		return true, nil
	}
	return false, nil
}

// unmarshalText decodes a string through encoding.TextUnmarshaler. It reports
// whether typ implements the interface.
func (d *Decoder) unmarshalText(text string, dst reflect.Value, path *pathStack) (bool, error) {
	typ := dst.Type()

	if typ.Kind() == reflect.Pointer {
		if !typ.Implements(textUnmarshalerType) {
			return false, nil
		}
		if dst.IsNil() {
			if !dst.CanSet() {
				return true, path.errorf("cannot allocate %s", typ)
			}
			dst.Set(reflect.New(typ.Elem()))
		}
		if err := dst.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
			return true, path.wrap(fmt.Errorf("cannot unmarshal %q into %s: %w", text, typ, err))
		}
		return true, nil
	}

	if !typ.Implements(textUnmarshalerType) && !reflect.PointerTo(typ).Implements(textUnmarshalerType) {
		return false, nil
	}

	pointer := reflect.New(typ)
	if err := pointer.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
		return true, path.wrap(fmt.Errorf("cannot unmarshal %q into %s: %w", text, typ, err))
	}
	dst.Set(pointer.Elem())
	return true, nil
}

// integralInt converts an integral float into an int64.
func integralInt(f float64) (int64, bool) {
	const maxInt64Exclusive = 9223372036854775808.0
	if math.IsNaN(f) || math.IsInf(f, 0) || f < math.MinInt64 || f >= maxInt64Exclusive || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

// integralUint converts an integral, non-negative float into a uint64.
func integralUint(f float64) (uint64, bool) {
	const maxUint64Exclusive = 18446744073709551616.0
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= maxUint64Exclusive || f != math.Trunc(f) {
		return 0, false
	}
	return uint64(f), true
}

// isWellKnownType reports whether typ, or a pointer chain ending at it, is one
// of the well-known named types with dedicated decoding rules.
func isWellKnownType(typ reflect.Type) bool {
	for range 8 {
		switch typ {
		case timeType, durationType, urlType:
			return true
		}
		if typ.Kind() != reflect.Pointer {
			return false
		}
		typ = typ.Elem()
	}
	return false
}

// isScalarKind reports whether values of kind are decoded from scalars.
func isScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// isNumericKind reports whether kind is an integer, unsigned integer or float.
func isNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}
