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

// overrideEntry keeps the caller's path casing (used when an override creates
// keys) while the override table key de-duplicates paths case-insensitively.
type overrideEntry struct {
	segments []string
	value    any
}

// SetOverride sets a runtime value at a dot-separated path in the root
// configuration and in every tenant configuration. Overrides take precedence
// over every source, are applied after placeholders are resolved, survive
// Reload, and live in memory until deleted or the process exits.
//
// Paths are matched case-insensitively. The reserved tenants section cannot be
// targeted directly; use SetTenantOverride for a single tenant.
func (l *Loader) SetOverride(configPath string, value any) error {
	entry, err := globalOverrideEntry(configPath, value)
	if err != nil {
		return err
	}
	return l.storeOverrideEntries("", []overrideEntry{entry})
}

// SetOverrides sets several global overrides with a single snapshot rebuild.
// Every path is validated before anything is applied, so an invalid path
// leaves the existing overrides untouched.
func (l *Loader) SetOverrides(values map[string]any) error {
	entries, err := globalOverrideEntries(values)
	if err != nil {
		return err
	}
	return l.storeOverrideEntries("", entries)
}

// SetTenantOverride sets a runtime value at a dot-separated path in one
// tenant configuration. Tenant codes are matched case-insensitively. If the
// tenant does not exist yet, the override is kept and applied when a reload
// introduces the tenant.
func (l *Loader) SetTenantOverride(tenantCode, configPath string, value any) error {
	if strings.TrimSpace(tenantCode) == "" {
		return fmt.Errorf("fastconf: tenant code must not be empty")
	}
	entry, err := tenantOverrideEntry(configPath, value)
	if err != nil {
		return err
	}
	return l.storeOverrideEntries(tenantCode, []overrideEntry{entry})
}

// SetTenantOverrides sets several overrides for one tenant with a single
// snapshot rebuild. Every path is validated before anything is applied.
func (l *Loader) SetTenantOverrides(tenantCode string, values map[string]any) error {
	if strings.TrimSpace(tenantCode) == "" {
		return fmt.Errorf("fastconf: tenant code must not be empty")
	}
	entries, err := tenantOverrideEntries(values)
	if err != nil {
		return err
	}
	return l.storeOverrideEntries(tenantCode, entries)
}

// Overrides returns a deep copy of the global runtime overrides, keyed by the
// dot-separated path as first set (case-insensitive matching still applies).
// It is intended for inspection surfaces such as the bundled server console.
func (l *Loader) Overrides() map[string]any {
	l.overridesMutex.RLock()
	defer l.overridesMutex.RUnlock()
	overrides := make(map[string]any, len(l.overrides))
	for _, entry := range l.overrides {
		overrides[strings.Join(entry.segments, ".")] = maps.DeepCopy(entry.value)
	}
	return overrides
}

// TenantOverrides returns a deep copy of the runtime overrides scoped to one
// tenant, keyed by the path as first set.
func (l *Loader) TenantOverrides(tenantCode string) map[string]any {
	l.overridesMutex.RLock()
	defer l.overridesMutex.RUnlock()
	values := l.tenantOverrides[strings.ToLower(tenantCode)]
	overrides := make(map[string]any, len(values))
	for _, entry := range values {
		overrides[strings.Join(entry.segments, ".")] = maps.DeepCopy(entry.value)
	}
	return overrides
}

// DeleteOverride removes a runtime override from the root path. It reports
// whether an override existed; source-provided values are restored.
func (l *Loader) DeleteOverride(configPath string) (bool, error) {
	keys, err := overrideDeletionKeys([]string{configPath}, true)
	if err != nil {
		return false, err
	}
	removed, err := l.removeOverrideEntries("", keys...)
	return removed > 0, err
}

// DeleteOverrides removes several global overrides with a single snapshot
// rebuild and reports how many existed.
func (l *Loader) DeleteOverrides(paths []string) (int, error) {
	keys, err := overrideDeletionKeys(paths, true)
	if err != nil {
		return 0, err
	}
	return l.removeOverrideEntries("", keys...)
}

// DeleteTenantOverride removes a runtime override from one tenant path and
// reports whether an override existed.
func (l *Loader) DeleteTenantOverride(tenantCode, configPath string) (bool, error) {
	if strings.TrimSpace(tenantCode) == "" {
		return false, fmt.Errorf("fastconf: tenant code must not be empty")
	}
	keys, err := overrideDeletionKeys([]string{configPath}, false)
	if err != nil {
		return false, err
	}
	removed, err := l.removeOverrideEntries(tenantCode, keys...)
	return removed > 0, err
}

// DeleteTenantOverrides removes several overrides from one tenant with a single
// snapshot rebuild and reports how many existed.
func (l *Loader) DeleteTenantOverrides(tenantCode string, paths []string) (int, error) {
	if strings.TrimSpace(tenantCode) == "" {
		return 0, fmt.Errorf("fastconf: tenant code must not be empty")
	}
	keys, err := overrideDeletionKeys(paths, false)
	if err != nil {
		return 0, err
	}
	return l.removeOverrideEntries(tenantCode, keys...)
}

func (l *Loader) removeOverrideEntries(tenantCode string, keys ...string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	l.loadMutex.Lock()
	defer l.loadMutex.Unlock()
	if l.closed.Load() {
		return 0, ErrClosed
	}

	removed := 0
	l.overridesMutex.Lock()
	if tenantCode == "" {
		for _, key := range keys {
			if _, ok := l.overrides[key]; ok {
				delete(l.overrides, key)
				removed++
			}
		}
	} else {
		tenantKey := strings.ToLower(tenantCode)
		if values, ok := l.tenantOverrides[tenantKey]; ok {
			for _, key := range keys {
				if _, exists := values[key]; exists {
					delete(values, key)
					removed++
				}
			}
			if len(values) == 0 {
				delete(l.tenantOverrides, tenantKey)
			}
		}
	}
	l.overridesMutex.Unlock()

	if removed > 0 {
		l.rebuildSnapshotLocked()
	}
	return removed, nil
}

func (l *Loader) storeOverrideEntries(tenantCode string, entries []overrideEntry) error {
	if len(entries) == 0 {
		return nil
	}
	l.loadMutex.Lock()
	defer l.loadMutex.Unlock()
	if l.closed.Load() {
		return ErrClosed
	}

	l.overridesMutex.Lock()
	if tenantCode == "" {
		if l.overrides == nil {
			l.overrides = make(map[string]overrideEntry)
		}
		for _, entry := range entries {
			key := canonicalOverridePath(entry.segments)
			if previous, ok := l.overrides[key]; ok {
				entry.segments = previous.segments
			}
			l.overrides[key] = entry
		}
	} else {
		tenantKey := strings.ToLower(tenantCode)
		if l.tenantOverrides == nil {
			l.tenantOverrides = make(map[string]map[string]overrideEntry)
		}
		values := l.tenantOverrides[tenantKey]
		if values == nil {
			values = make(map[string]overrideEntry)
			l.tenantOverrides[tenantKey] = values
		}
		for _, entry := range entries {
			key := canonicalOverridePath(entry.segments)
			if previous, ok := values[key]; ok {
				entry.segments = previous.segments
			}
			values[key] = entry
		}
	}
	l.overridesMutex.Unlock()

	l.rebuildSnapshotLocked()
	return nil
}

func (l *Loader) rebuildSnapshotLocked() {
	current := l.snapshot.Load()
	if current == nil {
		return
	}
	base := current.pristine
	if base == nil {
		base = current.document
	}
	l.snapshot.Store(l.buildSnapshot(base))
}

func globalOverrideEntry(configPath string, value any) (overrideEntry, error) {
	segments, err := overrideSegments(configPath)
	if err != nil {
		return overrideEntry{}, err
	}
	if strings.EqualFold(segments[0], sections.Tenants) {
		return overrideEntry{}, fmt.Errorf("fastconf: override path %q targets the reserved tenants section; use SetTenantOverride", configPath)
	}
	return overrideEntry{segments: segments, value: prepareOverrideValue(value)}, nil
}

func tenantOverrideEntry(configPath string, value any) (overrideEntry, error) {
	segments, err := overrideSegments(configPath)
	if err != nil {
		return overrideEntry{}, err
	}
	return overrideEntry{segments: segments, value: prepareOverrideValue(value)}, nil
}

func globalOverrideEntries(values map[string]any) ([]overrideEntry, error) {
	entries := make([]overrideEntry, 0, len(values))
	for configPath, value := range values {
		entry, err := globalOverrideEntry(configPath, value)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sortOverrideEntries(entries)
	return entries, nil
}

func tenantOverrideEntries(values map[string]any) ([]overrideEntry, error) {
	entries := make([]overrideEntry, 0, len(values))
	for configPath, value := range values {
		entry, err := tenantOverrideEntry(configPath, value)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sortOverrideEntries(entries)
	return entries, nil
}

// sortOverrideEntries applies shallow paths first so that deeper paths in one
// batch consistently win over a parent value.
func sortOverrideEntries(entries []overrideEntry) {
	slices.SortFunc(entries, func(a, b overrideEntry) int {
		if len(a.segments) != len(b.segments) {
			if len(a.segments) < len(b.segments) {
				return -1
			}
			return 1
		}
		return strings.Compare(canonicalOverridePath(a.segments), canonicalOverridePath(b.segments))
	})
}

func overrideDeletionKeys(paths []string, global bool) ([]string, error) {
	keys := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, configPath := range paths {
		segments, err := overrideSegments(configPath)
		if err != nil {
			return nil, err
		}
		if global && strings.EqualFold(segments[0], sections.Tenants) {
			return nil, fmt.Errorf("fastconf: override path %q targets the reserved tenants section; use DeleteTenantOverride", configPath)
		}
		key := canonicalOverridePath(segments)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
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
