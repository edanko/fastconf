package expr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/edanko/fastconf/internal/maps"
)

// Resolve resolves every placeholder template in a configuration root object in
// place.
//
// The document is the configuration root object itself, not a wrapper around
// it, so top-level keys become top-level paths. Strings without a placeholder
// marker are left untouched, nested objects and lists are visited recursively,
// and a list element written as *${path} is replaced by the elements of the
// list the placeholder points at.
//
// Paths are matched case-insensitively. When a path is not present in the
// document, Resolve falls back to an environment variable derived from the
// path: segments are joined with an underscore and upper-cased, so
// "database.host" reads DATABASE_HOST. Configuration values take precedence
// over environment variables.
//
// Errors are returned as *PathError values that carry the configuration path of
// the failing value, for example "servers[0].port".
func Resolve(ctx context.Context, values map[string]any) error {
	if values == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r := &resolver{ctx: ctx, root: values}
	return r.resolveObject(values, "")
}

// resolver resolves placeholders inside one configuration root object. It also
// implements Scope so templates can look paths up while they are evaluated.
type resolver struct {
	ctx  context.Context
	root map[string]any
}

// Lookup implements Scope: configuration values first, environment variables
// second.
func (r *resolver) Lookup(path string) (any, bool) {
	segments := strings.Split(path, ".")
	for i := range segments {
		segments[i] = strings.TrimSpace(segments[i])
	}
	if value, ok := maps.GetFold(r.root, segments); ok {
		return value, true
	}
	if name := environmentName(path); name != "" {
		return os.LookupEnv(name)
	}
	return nil, false
}

// resolveObject resolves every value of an object in place. prefix is the
// configuration path of object, empty for the root.
func (r *resolver) resolveObject(object map[string]any, prefix string) error {
	for key, value := range object {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		// The fast path avoids building a path and a scope lookup for strings
		// that cannot contain a placeholder.
		if text, ok := value.(string); ok && !hasMarkers(text) {
			continue
		}
		resolved, err := r.resolveValue(value, joinPath(prefix, key))
		if err != nil {
			return err
		}
		object[key] = resolved
	}
	return nil
}

// resolveValue resolves one configuration value and returns its replacement.
func (r *resolver) resolveValue(value any, path string) (any, error) {
	switch typed := value.(type) {
	case string:
		if !hasMarkers(typed) {
			return typed, nil
		}
		compiled, err := Compile(typed)
		if err != nil {
			return nil, &PathError{Path: path, Err: err}
		}
		resolved, err := compiled.Eval(r)
		if err != nil {
			return nil, &PathError{Path: path, Err: err}
		}
		return resolved, nil
	case map[string]any:
		if err := r.resolveObject(typed, path); err != nil {
			return nil, err
		}
		return typed, nil
	case []any:
		return r.resolveList(typed, path)
	default:
		return value, nil
	}
}

// resolveList resolves the elements of a list in place and returns the list
// with unpacked elements spliced in. The returned list may be a new slice when
// the input contains array unpack elements.
func (r *resolver) resolveList(list []any, path string) ([]any, error) {
	resolved := make([]any, 0, len(list))
	for i, element := range list {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		text, isString := element.(string)
		if !isString || !strings.HasPrefix(text, "*${") {
			resolved = append(resolved, element)
			continue
		}
		elementPath := indexPath(path, i)
		compiled, err := Compile(text)
		if err != nil {
			return nil, &PathError{Path: elementPath, Err: err}
		}
		if !compiled.Unpack() {
			// Compile refuses an unpack marker anywhere but at the start of a
			// value, so this is unreachable for "*${" prefixed strings.
			resolved = append(resolved, element)
			continue
		}
		value, err := compiled.Eval(r)
		if err != nil {
			return nil, &PathError{Path: elementPath, Err: err}
		}
		items, err := asList(value)
		if err != nil {
			return nil, &PathError{Path: elementPath, Err: fmt.Errorf("array unpack: %w", err)}
		}
		resolved = append(resolved, items...)
	}

	for i, element := range resolved {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		switch typed := element.(type) {
		case string:
			if !hasMarkers(typed) {
				continue
			}
		case map[string]any, []any:
			// Nested objects and lists must be resolved even when they hold no
			// markers themselves.
		default:
			continue
		}
		value, err := r.resolveValue(element, indexPath(path, i))
		if err != nil {
			return nil, err
		}
		resolved[i] = value
	}
	return resolved, nil
}

// asList converts a value produced by an unpack placeholder to a list.
func asList(value any) ([]any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, errors.New("value is nil, not a list")
	case []any:
		return typed, nil
	case []string:
		items := make([]any, len(typed))
		for i, item := range typed {
			items[i] = item
		}
		return items, nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		items := make([]any, reflected.Len())
		for i := range items {
			items[i] = reflected.Index(i).Interface()
		}
		return items, nil
	default:
		return nil, fmt.Errorf("value of type %T is not a list", value)
	}
}

// environmentName converts a placeholder path to an environment variable name:
// segments are joined with an underscore and upper-cased.
func environmentName(path string) string {
	var builder strings.Builder
	builder.Grow(len(path))
	wrote := false
	lastWasSeparator := true
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z':
			builder.WriteByte(c - 'a' + 'A')
			wrote = true
			lastWasSeparator = false
		case (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			builder.WriteByte(c)
			wrote = true
			lastWasSeparator = false
		default:
			if wrote && !lastWasSeparator {
				builder.WriteByte('_')
				lastWasSeparator = true
			}
		}
	}
	name := builder.String()
	if lastWasSeparator && len(name) > 0 {
		name = name[:len(name)-1]
	}
	return name
}

// joinPath appends an object key to a configuration path.
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// indexPath appends a list index to a configuration path.
func indexPath(prefix string, index int) string {
	return prefix + "[" + strconv.Itoa(index) + "]"
}
