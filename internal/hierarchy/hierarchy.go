// Package hierarchy implements the hierarchical configuration model.
//
// A document contains a configuration root with optional reserved sections:
// subsystems, environments and tenants. Collapse merges the active subsystem,
// application and environment into the root and into every tenant, then gives
// every tenant a complete copy of the root configuration. Resolve evaluates
// placeholder expressions in the root and every tenant.
package hierarchy

import (
	"context"
	"fmt"
	maps0 "maps"

	"github.com/edanko/fastconf/internal/expr"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
)

// Params selects the active configuration roles.
type Params struct {
	Subsystem   string
	Application string
	Environment string
}

// Initial returns an empty document containing the optional default tenant.
func Initial(defaultTenant string) map[string]any {
	root := map[string]any{}
	if defaultTenant != "" {
		root[sections.Tenants] = map[string]any{
			defaultTenant: map[string]any{sections.TenantCode: defaultTenant},
		}
	}
	return map[string]any{sections.Root: root}
}

// Config returns the configuration root of a document.
func Config(document map[string]any) (map[string]any, bool) {
	return maps.GetMap(document, []string{sections.Root})
}

// Collapse merges the active subsystem, application and environment sections
// into the configuration root and into every tenant.
func Collapse(document map[string]any, params Params) error {
	root, ok := Config(document)
	if !ok {
		return nil
	}
	collapseSubsystem(root, params.Subsystem, params.Application)
	if err := collapseTenantSubsystems(root, params.Subsystem, params.Application); err != nil {
		return fmt.Errorf("collapse tenant subsystems: %w", err)
	}
	if err := collapseEnvironment(root, params.Environment, params.Subsystem, params.Application); err != nil {
		return fmt.Errorf("collapse environment: %w", err)
	}
	return nil
}

// MergeRootIntoTenants merges a copy of the root into every tenant, so each
// tenant holds a complete configuration with tenant values taking precedence.
func MergeRootIntoTenants(document map[string]any) error {
	root, ok := Config(document)
	if !ok {
		return nil
	}
	tenants, ok := maps.GetMap(root, []string{sections.Tenants})
	if !ok {
		return nil
	}
	// Collect the root defaults once instead of deep-copying the whole root for
	// every tenant and then merging the tenant into that copy (two traversals
	// and a surplus map per tenant). Each tenant object is adopted by the
	// merged object: this load owns the document and tenants[code] is replaced,
	// so nothing else can observe the adopted containers. Root values are still
	// copied because every tenant shares them.
	type rootEntry struct {
		key   string
		value any
	}
	base := make([]rootEntry, 0, len(root))
	for key, value := range root {
		if key == sections.Tenants {
			continue
		}
		base = append(base, rootEntry{key: key, value: value})
	}
	for code, raw := range tenants {
		tenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tenant %q: expected an object, got %T", code, raw)
		}
		merged := make(map[string]any, len(base)+len(tenant))
		maps0.Copy(merged, tenant)
		for _, entry := range base {
			current, exists := merged[entry.key]
			if !exists {
				merged[entry.key] = maps.DeepCopy(entry.value)
				continue
			}
			currentObject, currentIsObject := current.(map[string]any)
			defaultObject, defaultIsObject := entry.value.(map[string]any)
			if currentIsObject && defaultIsObject {
				mergeDefaults(currentObject, defaultObject)
			}
		}
		tenants[code] = merged
	}
	return nil
}

// SetTenantCodes injects the tenant code into every tenant configuration.
func SetTenantCodes(document map[string]any) error {
	root, ok := Config(document)
	if !ok {
		return nil
	}
	tenants, ok := maps.GetMap(root, []string{sections.Tenants})
	if !ok {
		return nil
	}
	for code, raw := range tenants {
		tenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tenant %q: expected an object, got %T", code, raw)
		}
		tenant[sections.TenantCode] = code
	}
	return nil
}

// Resolve evaluates placeholder expressions in the root configuration and in
// every tenant configuration.
func Resolve(ctx context.Context, document map[string]any) error {
	root, ok := Config(document)
	if !ok {
		return nil
	}
	tenants, hasTenants := maps.ExtractMap(root, []string{sections.Tenants})

	if err := expr.Resolve(ctx, root); err != nil {
		return fmt.Errorf("resolve configuration: %w", err)
	}
	if !hasTenants {
		return nil
	}
	for code, raw := range tenants {
		tenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tenant %q: expected an object, got %T", code, raw)
		}
		if err := expr.Resolve(ctx, tenant); err != nil {
			return fmt.Errorf("resolve tenant %q: %w", code, err)
		}
		tenants[code] = tenant
	}
	root[sections.Tenants] = tenants
	return nil
}

func collapseSubsystem(root map[string]any, subsystem, application string) {
	subsystems, ok := maps.ExtractMap(root, []string{sections.Subsystems})
	if !ok {
		return
	}
	subsystemValues, ok := subsystems[subsystem].(map[string]any)
	if !ok {
		return
	}
	applications, _ := maps.ExtractMap(subsystemValues, []string{sections.Applications})

	maps.Merge(root, subsystemValues)

	if application == "" || applications == nil {
		return
	}
	applicationValues, ok := applications[application].(map[string]any)
	if !ok {
		return
	}
	maps.Merge(root, applicationValues)
}

func collapseTenantSubsystems(root map[string]any, subsystem, application string) error {
	tenants, ok := maps.GetMap(root, []string{sections.Tenants})
	if !ok {
		return nil
	}
	for code, raw := range tenants {
		tenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tenant %q: expected an object, got %T", code, raw)
		}
		subsystems, ok := maps.ExtractMap(tenant, []string{sections.Subsystems})
		if !ok {
			continue
		}
		subsystemValues, ok := subsystems[subsystem].(map[string]any)
		if !ok {
			continue
		}
		applications, _ := maps.ExtractMap(subsystemValues, []string{sections.Applications})

		maps.Merge(tenant, subsystemValues)

		if application == "" || applications == nil {
			continue
		}
		applicationValues, ok := applications[application].(map[string]any)
		if !ok {
			continue
		}
		maps.Merge(tenant, applicationValues)
	}
	return nil
}

func collapseEnvironment(root map[string]any, environment, subsystem, application string) error {
	environments, ok := maps.ExtractMap(root, []string{sections.Environments})
	if !ok || environment == "" {
		return nil
	}
	environmentValues, ok := environments[environment].(map[string]any)
	if !ok {
		return nil
	}
	environmentTenants, _ := maps.ExtractMap(environmentValues, []string{sections.Tenants})

	if err := collapseInto(root, environmentValues, subsystem, application); err != nil {
		return err
	}

	tenants, _ := maps.GetMap(root, []string{sections.Tenants})
	for code, raw := range tenants {
		tenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tenant %q: expected an object, got %T", code, raw)
		}
		if err := collapseInto(tenant, environmentValues, subsystem, application); err != nil {
			return err
		}
	}

	if environmentTenants == nil {
		return nil
	}
	if tenants == nil {
		tenants = map[string]any{}
		root[sections.Tenants] = tenants
	}
	for code, raw := range environmentTenants {
		environmentTenant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("environment tenant %q: expected an object, got %T", code, raw)
		}
		tenant, ok := tenants[code].(map[string]any)
		if !ok {
			tenant = map[string]any{}
			tenants[code] = tenant
		}
		if err := collapseInto(tenant, environmentValues, subsystem, application); err != nil {
			return err
		}
		if err := collapseInto(tenant, environmentTenant, subsystem, application); err != nil {
			return err
		}
	}
	return nil
}

// collapseInto merges source values into destination, merging the source
// subsystem and application sections into the destination root.
func collapseInto(destination, source map[string]any, subsystem, application string) error {
	sourceCopy := maps.DeepCopyMap(source)
	subsystems, _ := maps.ExtractMap(sourceCopy, []string{sections.Subsystems})

	maps.Merge(destination, sourceCopy)

	if subsystem == "" || subsystems == nil {
		return nil
	}
	subsystemValues, ok := subsystems[subsystem].(map[string]any)
	if !ok {
		return nil
	}
	applications, _ := maps.ExtractMap(subsystemValues, []string{sections.Applications})

	maps.Merge(destination, subsystemValues)

	if application == "" || applications == nil {
		return nil
	}
	applicationValues, ok := applications[application].(map[string]any)
	if !ok {
		return nil
	}
	maps.Merge(destination, applicationValues)
	return nil
}

// mergeDefaults fills target with the keys of defaults that target does not
// already have, recursing when both sides hold objects. target wins every
// conflict, matching maps.Merge(deepCopy(defaults), target) without copying
// target.
func mergeDefaults(target, defaults map[string]any) {
	for key, defaultValue := range defaults {
		current, exists := target[key]
		if exists {
			currentObject, currentIsObject := current.(map[string]any)
			defaultObject, defaultIsObject := defaultValue.(map[string]any)
			if currentIsObject && defaultIsObject {
				mergeDefaults(currentObject, defaultObject)
			}
			continue
		}
		target[key] = maps.DeepCopy(defaultValue)
	}
}
