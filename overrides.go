package fastconf

import (
	"fmt"
	stdmaps "maps"
	"slices"
	"strings"

	"github.com/edanko/fastconf/encoding"
	"github.com/edanko/fastconf/internal/hierarchy"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
)

// SetOverride sets a runtime value at a dot-separated path in the root
// configuration and in every tenant configuration. Overrides take precedence
// over every source, are applied after placeholders are resolved, survive
// Reload, and live in memory until deleted or the process exits.
//
// Paths are matched case-insensitively. The reserved tenants section cannot be
// targeted directly; use SetTenantOverride for a single tenant.
func (l *Loader) SetOverride(configPath string, value any) error {
	segments, err := overrideSegments(configPath)
	if err != nil {
		return err
	}
	if strings.EqualFold(segments[0], sections.Tenants) {
		return fmt.Errorf("fastconf: override path %q targets the reserved tenants section; use SetTenantOverride", configPath)
	}
	_, err = l.updateOverride("", segments, prepareOverrideValue(value), false)
	return err
}

// SetTenantOverride sets a runtime value at a dot-separated path in one
// tenant configuration. Tenant codes are matched case-insensitively. If the
// tenant does not exist yet, the override is kept and applied when a reload
// introduces the tenant.
func (l *Loader) SetTenantOverride(tenantCode, configPath string, value any) error {
	if strings.TrimSpace(tenantCode) == "" {
		return fmt.Errorf("fastconf: tenant code must not be empty")
	}
	segments, err := overrideSegments(configPath)
	if err != nil {
		return err
	}
	_, err = l.updateOverride(tenantCode, segments, prepareOverrideValue(value), false)
	return err
}

// DeleteOverride removes a runtime override from the root path. It reports
// whether an override existed; source-provided values are restored.
func (l *Loader) DeleteOverride(configPath string) (bool, error) {
	segments, err := overrideSegments(configPath)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(segments[0], sections.Tenants) {
		return false, fmt.Errorf("fastconf: override path %q targets the reserved tenants section; use DeleteTenantOverride", configPath)
	}
	return l.updateOverride("", segments, nil, true)
}

// DeleteTenantOverride removes a runtime override from one tenant path and
// reports whether an override existed.
func (l *Loader) DeleteTenantOverride(tenantCode, configPath string) (bool, error) {
	if strings.TrimSpace(tenantCode) == "" {
		return false, fmt.Errorf("fastconf: tenant code must not be empty")
	}
	segments, err := overrideSegments(configPath)
	if err != nil {
		return false, err
	}
	return l.updateOverride(tenantCode, segments, nil, true)
}

// overrideEntry keeps the caller's path casing (used when an override creates
// keys) while the map key de-duplicates paths case-insensitively.
type overrideEntry struct {
	segments []string
	value    any
}

// updateOverride mutates the override tables and rebuilds the published
// snapshot from the pristine document. loadMutex serializes it with loads and
// reloads; readers keep using the previous immutable snapshot until the swap.
func (l *Loader) updateOverride(tenantCode string, segments []string, value any, remove bool) (bool, error) {
	l.loadMutex.Lock()
	defer l.loadMutex.Unlock()
	if l.closed.Load() {
		return false, ErrClosed
	}

	changed := false
	l.overridesMutex.Lock()
	if tenantCode == "" {
		key := canonicalOverridePath(segments)
		if remove {
			if _, ok := l.overrides[key]; ok {
				delete(l.overrides, key)
				changed = true
			}
		} else {
			if l.overrides == nil {
				l.overrides = make(map[string]overrideEntry)
			}
			entry := overrideEntry{segments: segments, value: value}
			if previous, ok := l.overrides[key]; ok {
				// Keep the casing of the path that created the keys.
				entry.segments = previous.segments
			}
			l.overrides[key] = entry
			changed = true
		}
	} else {
		tenantKey := strings.ToLower(tenantCode)
		if remove {
			if values, ok := l.tenantOverrides[tenantKey]; ok {
				key := canonicalOverridePath(segments)
				if _, exists := values[key]; exists {
					delete(values, key)
					changed = true
					if len(values) == 0 {
						delete(l.tenantOverrides, tenantKey)
					}
				}
			}
		} else {
			if l.tenantOverrides == nil {
				l.tenantOverrides = make(map[string]map[string]overrideEntry)
			}
			values := l.tenantOverrides[tenantKey]
			if values == nil {
				values = make(map[string]overrideEntry)
				l.tenantOverrides[tenantKey] = values
			}
			key := canonicalOverridePath(segments)
			entry := overrideEntry{segments: segments, value: value}
			if previous, ok := values[key]; ok {
				entry.segments = previous.segments
			}
			values[key] = entry
			changed = true
		}
	}
	l.overridesMutex.Unlock()

	if !changed {
		return false, nil
	}
	if current := l.snapshot.Load(); current != nil {
		base := current.pristine
		if base == nil {
			base = current.document
		}
		l.snapshot.Store(l.buildSnapshot(base))
	}
	return true, nil
}

// buildSnapshot applies the current overrides to a copy-on-write view of base.
// The base document stays pristine and immutable; only maps along override
// paths are copied, so untouched tenants and subtrees are shared between the
// pristine document and the published snapshot.
func (l *Loader) buildSnapshot(base map[string]any) *snapshot {
	l.overridesMutex.RLock()
	hasOverrides := len(l.overrides) > 0 || len(l.tenantOverrides) > 0
	overrides := l.overrides
	tenantOverrides := l.tenantOverrides
	l.overridesMutex.RUnlock()
	if !hasOverrides {
		return newSnapshot(base)
	}
	current := newSnapshot(applyOverridesCopy(base, overrides, tenantOverrides))
	current.pristine = base
	return current
}

// applyOverridesCopy returns a document with the overrides applied. Every map
// that changes is shallow-cloned before it is modified; all other maps are
// shared with base, which is never mutated. Override values themselves are
// immutable and may be shared by every tenant.
func applyOverridesCopy(base map[string]any, overrides map[string]overrideEntry, tenantOverrides map[string]map[string]overrideEntry) map[string]any {
	applied := stdmaps.Clone(base)
	root, ok := hierarchy.Config(applied)
	if !ok {
		return applied
	}
	rootCopy := stdmaps.Clone(root)
	applied[sections.Root] = rootCopy

	rawTenants, hasTenants := root[sections.Tenants].(map[string]any)
	if !hasTenants {
		for _, entry := range overrides {
			setPathCopy(rootCopy, entry.segments, entry.value)
		}
		return applied
	}

	tenantsCopy := stdmaps.Clone(rawTenants)
	rootCopy[sections.Tenants] = tenantsCopy
	index := tenantOverridesByCode(tenantOverrides)
	for code, rawTenant := range rawTenants {
		tenant, ok := rawTenant.(map[string]any)
		if !ok {
			continue
		}
		entries := index[strings.ToLower(code)]
		if len(overrides) == 0 && len(entries) == 0 {
			continue
		}
		tenantCopy := stdmaps.Clone(tenant)
		for _, entry := range overrides {
			setPathCopy(tenantCopy, entry.segments, entry.value)
		}
		for _, entry := range entries {
			setPathCopy(tenantCopy, entry.segments, entry.value)
		}
		tenantsCopy[code] = tenantCopy
	}
	for _, entry := range overrides {
		setPathCopy(rootCopy, entry.segments, entry.value)
	}
	return applied
}

// setPathCopy sets a value in object, cloning every map it descends through so
// shared maps from the pristine document are never mutated.
func setPathCopy(object map[string]any, segments []string, value any) {
	if len(segments) == 0 {
		return
	}
	current := object
	for _, segment := range segments[:len(segments)-1] {
		storedKey, child, found := lookupFold(current, segment)
		if found {
			if childObject, ok := child.(map[string]any); ok {
				cloned := stdmaps.Clone(childObject)
				current[storedKey] = cloned
				current = cloned
				continue
			}
			childObject := make(map[string]any, 4)
			current[storedKey] = childObject
			current = childObject
			continue
		}
		childObject := make(map[string]any, 4)
		current[segment] = childObject
		current = childObject
	}
	last := segments[len(segments)-1]
	if storedKey, _, found := lookupFold(current, last); found {
		current[storedKey] = value
		return
	}
	current[last] = value
}

func lookupFold(object map[string]any, key string) (string, any, bool) {
	if value, ok := object[key]; ok {
		return key, value, true
	}
	for candidate, value := range object {
		if strings.EqualFold(candidate, key) {
			return candidate, value, true
		}
	}
	return "", nil, false
}

func tenantOverridesByCode(tenantOverrides map[string]map[string]overrideEntry) map[string][]overrideEntry {
	index := make(map[string][]overrideEntry, len(tenantOverrides))
	for code, values := range tenantOverrides {
		entries := make([]overrideEntry, 0, len(values))
		for _, entry := range values {
			entries = append(entries, entry)
		}
		index[code] = entries
	}
	return index
}

func overrideSegments(configPath string) ([]string, error) {
	segments := strings.Split(configPath, ".")
	if len(segments) == 0 || strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("fastconf: override path must not be empty")
	}
	if slices.Contains(segments, "") {
		return nil, fmt.Errorf("fastconf: override path %q has an empty segment", configPath)
	}
	return segments, nil
}

func canonicalOverridePath(segments []string) string {
	lowered := make([]string, len(segments))
	for index, segment := range segments {
		lowered[index] = strings.ToLower(segment)
	}
	return strings.Join(lowered, ".")
}

func prepareOverrideValue(value any) any {
	return encoding.Normalize(maps.DeepCopy(value))
}
