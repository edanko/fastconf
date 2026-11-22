package value

import "strings"

// Object is an immutable ordered set of named configuration values.
//
// Keys preserve the case of the source document. Lookups are exact first and
// case-insensitive second, matching the behaviour of configuration sources.
type Object struct {
	keys []string
	vals []Value
}

// NewObject creates an object with the given capacity.
func NewObject(capacity int) *Object {
	return &Object{
		keys: make([]string, 0, capacity),
		vals: make([]Value, 0, capacity),
	}
}

// FromMap converts a decoded document object into an Object tree.
func FromMap(m map[string]any) *Object {
	object := NewObject(len(m))
	for key, raw := range m {
		object.keys = append(object.keys, key)
		object.vals = append(object.vals, From(raw))
	}
	return object
}

// Len returns the number of values.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// At returns the key and value at index i. Keys are returned in insertion
// order, which follows the source document iteration order.
func (o *Object) At(i int) (string, Value) {
	return o.keys[i], o.vals[i]
}

// Get returns the value stored under name. Exact matches are preferred over
// case-insensitive matches.
func (o *Object) Get(name string) (Value, bool) {
	if o == nil {
		return Value{}, false
	}
	for i, key := range o.keys {
		if key == name {
			return o.vals[i], true
		}
	}
	for i, key := range o.keys {
		if strings.EqualFold(key, name) {
			return o.vals[i], true
		}
	}
	return Value{}, false
}

// GetFold returns the value stored under name using case-insensitive matching.
func (o *Object) GetFold(name string) (Value, bool) {
	if o == nil {
		return Value{}, false
	}
	for i, key := range o.keys {
		if strings.EqualFold(key, name) {
			return o.vals[i], true
		}
	}
	return Value{}, false
}

// Keys returns the object keys. The returned slice must not be modified.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}
