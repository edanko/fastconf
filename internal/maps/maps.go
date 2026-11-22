// Package maps contains helpers for configuration documents represented as
// nested map[string]any values.
//
// Configuration documents are always trees of string-keyed objects, slices and
// scalar values (the subset produced by JSON, YAML and TOML decoders). Helpers
// in this package intentionally avoid reflection: every operation is a direct
// type switch so it stays cheap on large documents.
package maps

import (
	stdmaps "maps"
	"slices"
	"strings"
)

// Get returns the value located at path.
func Get(values map[string]any, path []string) (any, bool) {
	var current any = values
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// GetMap returns the object located at path.
func GetMap(values map[string]any, path []string) (map[string]any, bool) {
	value, ok := Get(values, path)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// GetString returns the string located at path.
func GetString(values map[string]any, path []string) (string, bool) {
	value, ok := Get(values, path)
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	return str, ok
}

// GetFold is Get with case-insensitive key matching.
func GetFold(values map[string]any, path []string) (any, bool) {
	var current any = values
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = getFold(object, key)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// GetMapFold is GetMap with case-insensitive key matching.
func GetMapFold(values map[string]any, path []string) (map[string]any, bool) {
	value, ok := GetFold(values, path)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// Has reports whether path exists.
func Has(values map[string]any, path []string) bool {
	_, ok := Get(values, path)
	return ok
}

// Set stores value at path, creating intermediate objects as needed.
func Set(values map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return errEmptyPath
	}
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := current[key]
		if !ok {
			child := make(map[string]any, 4)
			current[key] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return &notAnObjectError{key: key}
		}
		current = child
	}
	current[path[len(path)-1]] = value
	return nil
}

// SetFold is Set with case-insensitive key matching.
func SetFold(values map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return errEmptyPath
	}
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := getFold(current, key)
		if !ok {
			child := make(map[string]any, 4)
			current[key] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return &notAnObjectError{key: key}
		}
		current = child
	}
	setFold(current, path[len(path)-1], value)
	return nil
}

// SetFoldForce is SetFold but replaces non-object intermediate values with
// objects instead of failing. Runtime overrides use it because an override is
// expected to win over a conflicting scalar.
func SetFoldForce(values map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return errEmptyPath
	}
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := getFold(current, key)
		if ok {
			if child, isObject := next.(map[string]any); isObject {
				current = child
				continue
			}
		}
		child := make(map[string]any, 4)
		if storedKey, _, exists := getFoldKey(current, key); exists {
			current[storedKey] = child
		} else {
			current[key] = child
		}
		current = child
	}
	setFold(current, path[len(path)-1], value)
	return nil
}

// Delete removes path from values and returns the removed value.
func Delete(values map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := current[key]
		if !ok {
			return nil, false
		}
		child, ok := next.(map[string]any)
		if !ok {
			return nil, false
		}
		current = child
	}
	key := path[len(path)-1]
	value, ok := current[key]
	if !ok {
		return nil, false
	}
	delete(current, key)
	return value, true
}

// DeleteFold removes path from values with case-insensitive key matching and
// returns the removed value.
func DeleteFold(values map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := getFold(current, key)
		if !ok {
			return nil, false
		}
		child, ok := next.(map[string]any)
		if !ok {
			return nil, false
		}
		current = child
	}
	storedKey, value, ok := getFoldKey(current, path[len(path)-1])
	if !ok {
		return nil, false
	}
	delete(current, storedKey)
	return value, true
}

// ExtractMap removes the object at path and returns it.
func ExtractMap(values map[string]any, path []string) (map[string]any, bool) {
	value, ok := Delete(values, path)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return object, true
}

// Merge deeply merges src into dst. Source objects are merged recursively,
// every other source value overrides the destination value and is deep-copied
// so that later mutations of either document cannot affect the other.
func Merge(dst, src map[string]any) {
	for key, srcValue := range src {
		dstValue, exists := dst[key]
		if exists {
			dstObject, dstIsObject := dstValue.(map[string]any)
			srcObject, srcIsObject := srcValue.(map[string]any)
			if dstIsObject && srcIsObject {
				Merge(dstObject, srcObject)
				continue
			}
		}
		dst[key] = DeepCopy(srcValue)
	}
}

// DeepCopy returns a deep copy of a configuration value.
func DeepCopy(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return DeepCopyMap(typed)
	case []any:
		return deepCopyList(typed)
	default:
		return value
	}
}

// DeepCopyMap returns a deep copy of a configuration document.
//
// The map itself is copied with maps.Clone, which uses the runtime's bulk map
// copy instead of inserting every key again; only nested maps and lists are
// replaced with their own copies. Cloning first also allocates the same map
// shape as the source, so no growth rehashing happens while replacing values.
func DeepCopyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := stdmaps.Clone(values)
	for key, value := range clone {
		switch typed := value.(type) {
		case map[string]any:
			clone[key] = DeepCopyMap(typed)
		case []any:
			clone[key] = deepCopyList(typed)
		}
	}
	return clone
}

func deepCopyList(values []any) []any {
	if values == nil {
		return nil
	}
	clone := slices.Clone(values)
	for index, value := range clone {
		switch typed := value.(type) {
		case map[string]any:
			clone[index] = DeepCopyMap(typed)
		case []any:
			clone[index] = deepCopyList(typed)
		}
	}
	return clone
}

func getFold(values map[string]any, key string) (any, bool) {
	_, value, ok := getFoldKey(values, key)
	return value, ok
}

// getFoldKey returns the stored key and value matching key case-insensitively.
// An exact match wins over a folded one.
func getFoldKey(values map[string]any, key string) (string, any, bool) {
	if value, ok := values[key]; ok {
		return key, value, true
	}
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) {
			return candidate, value, true
		}
	}
	return "", nil, false
}

func setFold(values map[string]any, key string, value any) {
	if _, ok := values[key]; ok {
		values[key] = value
		return
	}
	for candidate := range values {
		if strings.EqualFold(candidate, key) {
			values[candidate] = value
			return
		}
	}
	values[key] = value
}
