// Package sections defines the reserved configuration section names used by
// the hierarchical configuration model.
package sections

const (
	// Root is the top-level document key that holds the configuration root.
	Root = "config"
	// Subsystems holds subsystem scoped configuration.
	Subsystems = "subsystems"
	// Applications holds application scoped configuration inside a subsystem.
	Applications = "applications"
	// Tenants holds tenant scoped configuration keyed by tenant code.
	Tenants = "tenants"
	// TenantCode holds the injected tenant code inside a tenant configuration.
	TenantCode = "tenantCode"
	// Environments holds environment scoped configuration.
	Environments = "environments"
)
