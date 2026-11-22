package fastconf

import (
	"context"
	"strconv"
	"strings"

	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/value"
)

// View is an immutable, consistent read handle on one loaded configuration
// snapshot.
//
// A View pins the snapshot that was current when it was created: a later
// Reload does not change values read through the View, and several reads
// through one View always observe the same configuration. Decoding into
// structs and reading scalar values do not copy configuration data, which
// makes a View the right handle for per-request reads. Containers returned by
// Get are copied so callers may modify them freely; use Map or TenantMap when
// an entire subtree is needed as a mutable map.
//
// A View is safe for concurrent use.
type View struct {
	loader   *Loader
	snapshot *snapshot
	config   map[string]any
	value    value.Value
}

// View returns a read handle on the current configuration snapshot.
func (l *Loader) View(ctx context.Context) (*View, error) {
	current, err := l.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	current.ensureTree()
	return &View{
		loader:   l,
		snapshot: current,
		config:   current.config,
		value:    current.rootValue,
	}, nil
}

// Decode decodes the viewed configuration scope into destination.
func (v *View) Decode(destination any) error {
	if err := v.loader.decoder.Decode(&v.value, destination); err != nil {
		return err
	}
	return v.loader.validate(destination)
}

// Tenant returns a view scoped to one tenant configuration. Tenant codes are
// matched case-insensitively.
func (v *View) Tenant(tenantCode string) (*View, bool) {
	tenant, ok := v.snapshot.tenant(tenantCode)
	if !ok {
		return nil, false
	}
	raw, ok := v.snapshot.tenantMap(tenantCode)
	if !ok {
		return nil, false
	}
	return &View{
		loader:   v.loader,
		snapshot: v.snapshot,
		config:   raw,
		value:    value.ObjectValue(tenant),
	}, true
}

// TenantCodes returns a copy of the sorted tenant codes in the snapshot.
func (v *View) TenantCodes() []string {
	return append([]string(nil), v.snapshot.tenantCodes...)
}

// Get returns the value at a dot-separated path with case-insensitive segments.
//
// Scalar values are returned without copying. Objects and lists are returned
// as deep copies, so the caller may modify them without affecting the
// snapshot. Use Map to obtain the whole scope as a mutable map.
func (v *View) Get(configPath string) (any, bool) {
	raw, ok := v.lookup(configPath)
	if !ok {
		return nil, false
	}
	switch raw.(type) {
	case map[string]any, []any:
		return maps.DeepCopy(raw), true
	default:
		return raw, true
	}
}

// Has reports whether the path exists in the viewed scope.
func (v *View) Has(configPath string) bool {
	_, ok := v.lookup(configPath)
	return ok
}

// String returns the string stored at path.
func (v *View) String(configPath string) (string, bool) {
	raw, ok := v.lookup(configPath)
	if !ok {
		return "", false
	}
	switch typed := raw.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

// Int returns the integer stored at path. Numeric strings and floats are
// converted; non-integral values report false only when they cannot be
// represented.
func (v *View) Int(configPath string) (int64, bool) {
	raw, ok := v.lookup(configPath)
	if !ok {
		return 0, false
	}
	switch typed := raw.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// Float64 returns the floating point value stored at path.
func (v *View) Float64(configPath string) (float64, bool) {
	raw, ok := v.lookup(configPath)
	if !ok {
		return 0, false
	}
	switch typed := raw.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// Bool returns the boolean value stored at path.
func (v *View) Bool(configPath string) (bool, bool) {
	raw, ok := v.lookup(configPath)
	if !ok {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

// Map returns a deep copy of the viewed scope as a mutable map.
func (v *View) Map() map[string]any {
	return maps.DeepCopyMap(v.config)
}

// lookup resolves a dot-separated path without allocating for the common
// short path: segments are collected in a stack buffer and the slice is only
// appended to (which may grow) for deeper paths.
func (v *View) lookup(configPath string) (any, bool) {
	var buffer [8]string
	segments := buffer[:0]
	for {
		segment, rest, found := strings.Cut(configPath, ".")
		segments = append(segments, segment)
		if !found {
			break
		}
		configPath = rest
	}
	return maps.GetFold(v.config, segments)
}
