// Package value provides an immutable configuration value tree used by the
// decoding hot path.
//
// Documents are converted once into this tree. After that, struct decoding,
// tenant lookup and path lookups are lock-free read operations with no
// interface boxing on scalar values.
package value

import (
	"fmt"
	"math"
	"strconv"
)

// Kind identifies the type held by a Value.
type Kind uint8

// Kind values.
const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	KindList
	KindObject
	KindAny
)

// String returns the name of the kind.
func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindString:
		return "string"
	case KindList:
		return "list"
	case KindObject:
		return "object"
	case KindAny:
		return "any"
	default:
		return "kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Value is a single configuration value.
//
// The zero Value is null. Values are immutable: every constructor copies the
// slices it is given.
type Value struct {
	kind Kind
	b    bool
	i    int64
	f    float64
	s    string
	list []Value
	obj  *Object
	any  any
}

// Null returns a null value.
func Null() Value { return Value{} }

// Bool returns a boolean value.
func Bool(v bool) Value { return Value{kind: KindBool, b: v} }

// Int returns an integer value.
func Int(v int64) Value { return Value{kind: KindInt, i: v} }

// Float returns a floating point value.
func Float(v float64) Value { return Value{kind: KindFloat, f: v} }

// String returns a string value.
func String(v string) Value { return Value{kind: KindString, s: v} }

// List returns a list value. The given slice is referenced, not copied.
func List(v []Value) Value { return Value{kind: KindList, list: v} }

// ObjectValue returns an object value.
func ObjectValue(o *Object) Value {
	if o == nil {
		return Null()
	}
	return Value{kind: KindObject, obj: o}
}

// Any returns a value that holds an arbitrary Go value.
func Any(v any) Value {
	if v == nil {
		return Null()
	}
	return Value{kind: KindAny, any: v}
}

// From converts an arbitrary decoded Go value into a Value.
func From(v any) Value {
	switch typed := v.(type) {
	case nil:
		return Null()
	case bool:
		return Bool(typed)
	case string:
		return String(typed)
	case int:
		return Int(int64(typed))
	case int8:
		return Int(int64(typed))
	case int16:
		return Int(int64(typed))
	case int32:
		return Int(int64(typed))
	case int64:
		return Int(typed)
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return Float(float64(typed))
		}
		return Int(int64(typed))
	case uint8:
		return Int(int64(typed))
	case uint16:
		return Int(int64(typed))
	case uint32:
		return Int(int64(typed))
	case uint64:
		if typed > math.MaxInt64 {
			return Float(float64(typed))
		}
		return Int(int64(typed))
	case float32:
		return Float(float64(typed))
	case float64:
		return Float(typed)
	case []any:
		items := make([]Value, len(typed))
		for i, item := range typed {
			items[i] = From(item)
		}
		return List(items)
	case map[string]any:
		return ObjectValue(FromMap(typed))
	default:
		return Any(v)
	}
}

// Kind returns the value kind.
func (v Value) Kind() Kind { return v.kind }

// IsNull reports whether the value is null.
func (v Value) IsNull() bool { return v.kind == KindNull }

// Bool returns the value as a bool. The caller must check the kind first.
func (v Value) Bool() bool { return v.b }

// Int returns the value as an int64. The caller must check the kind first.
func (v Value) Int() int64 { return v.i }

// Float returns the value as a float64.
func (v Value) Float() float64 {
	if v.kind == KindInt {
		return float64(v.i)
	}
	return v.f
}

// String returns the value as a string.
func (v Value) String() string {
	switch v.kind {
	case KindString:
		return v.s
	case KindInt:
		return strconv.FormatInt(v.i, 10)
	case KindFloat:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(v.b)
	case KindNull:
		return ""
	default:
		return fmt.Sprint(v.Interface())
	}
}

// List returns the value as a list. The returned slice must not be modified.
func (v Value) List() []Value { return v.list }

// Object returns the value as an object.
func (v Value) Object() *Object { return v.obj }

// AnyValue returns the wrapped arbitrary value.
func (v Value) AnyValue() any { return v.any }

// IsScalar reports whether the value is a scalar.
func (v Value) IsScalar() bool {
	switch v.kind {
	case KindBool, KindInt, KindFloat, KindString, KindNull:
		return true
	default:
		return false
	}
}

// Interface converts the value into a plain Go value, deep-copying containers.
func (v Value) Interface() any {
	switch v.kind {
	case KindNull:
		return nil
	case KindBool:
		return v.b
	case KindInt:
		return v.i
	case KindFloat:
		return v.f
	case KindString:
		return v.s
	case KindList:
		items := make([]any, len(v.list))
		for i, item := range v.list {
			items[i] = item.Interface()
		}
		return items
	case KindObject:
		object := make(map[string]any, v.obj.Len())
		for i := 0; i < v.obj.Len(); i++ {
			key, item := v.obj.At(i)
			object[key] = item.Interface()
		}
		return object
	default:
		return v.any
	}
}
