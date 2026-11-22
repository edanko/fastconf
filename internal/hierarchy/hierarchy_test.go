package hierarchy

import (
	"context"
	"reflect"
	"testing"

	"github.com/edanko/fastconf/internal/doc"
	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
)

func document(values map[string]any) map[string]any {
	return doc.Normalize(maps.DeepCopyMap(values))
}

func rootOf(t *testing.T, values map[string]any) map[string]any {
	t.Helper()
	root, ok := Config(values)
	if !ok {
		t.Fatal("configuration root is missing")
	}
	return root
}

func TestCollapseSubsystemAndApplication(t *testing.T) {
	values := document(map[string]any{
		"config": map[string]any{
			"domain": "local",
			"database": map[string]any{
				"host": "root-db",
				"port": 5432,
			},
			"subsystems": map[string]any{
				"payments": map[string]any{
					"database": map[string]any{
						"host": "subsystem-db",
					},
					"applications": map[string]any{
						"billing": map[string]any{
							"database": map[string]any{
								"port": 6432,
							},
							"feature": true,
						},
					},
				},
			},
		},
	})

	err := Collapse(values, Params{Subsystem: "payments", Application: "billing"})
	if err != nil {
		t.Fatalf("Collapse: %v", err)
	}

	root := rootOf(t, values)
	want := map[string]any{
		"domain": "local",
		"database": map[string]any{
			"host": "subsystem-db",
			"port": float64(6432),
		},
		"feature": true,
	}
	if got := root["database"].(map[string]any); got["host"] != "subsystem-db" || got["port"] != 6432 {
		t.Fatalf("database = %#v", got)
	}
	if _, ok := root["subsystems"]; ok {
		t.Fatal("subsystems section must be removed")
	}
	if root["feature"] != true {
		t.Fatalf("application values were not merged: %#v", want)
	}
}

func TestCollapseTenantSubsystems(t *testing.T) {
	values := document(map[string]any{
		"config": map[string]any{
			"subsystems": map[string]any{
				"payments": map[string]any{
					"applications": map[string]any{
						"billing": map[string]any{"feature": "app"},
					},
					"shared": "subsystem",
				},
			},
			"tenants": map[string]any{
				"acme": map[string]any{
					"domain": "acme.example",
					"subsystems": map[string]any{
						"payments": map[string]any{
							"shared": "tenant-subsystem",
							"applications": map[string]any{
								"billing": map[string]any{"feature": "tenant-app"},
							},
						},
					},
				},
			},
		},
	})

	if err := Collapse(values, Params{Subsystem: "payments", Application: "billing"}); err != nil {
		t.Fatalf("Collapse: %v", err)
	}

	tenants, _ := maps.GetMap(rootOf(t, values), []string{"tenants"})
	acme := tenants["acme"].(map[string]any)
	if acme["shared"] != "tenant-subsystem" {
		t.Fatalf("tenant subsystem values were not collapsed: %#v", acme)
	}
	if acme["feature"] != "tenant-app" {
		t.Fatalf("tenant application values were not collapsed: %#v", acme)
	}
	if _, ok := acme["subsystems"]; ok {
		t.Fatal("tenant subsystems section must be removed")
	}
}

func TestCollapseEnvironment(t *testing.T) {
	values := document(map[string]any{
		"config": map[string]any{
			"domain": "local",
			"database": map[string]any{
				"host": "root-db",
			},
			"environments": map[string]any{
				"production": map[string]any{
					"domain": "production.example",
					"database": map[string]any{
						"host": "environment-db",
					},
					"subsystems": map[string]any{
						"payments": map[string]any{
							"database": map[string]any{
								"port": 6432,
							},
						},
					},
					"tenants": map[string]any{
						"acme": map[string]any{
							"domain": "acme.production.example",
						},
						"newco": map[string]any{
							"domain": "newco.production.example",
						},
					},
				},
			},
			"tenants": map[string]any{
				"acme": map[string]any{
					"database": map[string]any{"user": "acme"},
				},
			},
		},
	})

	if err := Collapse(values, Params{Subsystem: "payments", Environment: "production"}); err != nil {
		t.Fatalf("Collapse: %v", err)
	}

	root := rootOf(t, values)
	if root["domain"] != "production.example" {
		t.Fatalf("root environment values were not merged: %#v", root)
	}
	if database := root["database"].(map[string]any); database["host"] != "environment-db" || database["port"] != 6432 {
		t.Fatalf("root environment subsystem values were not merged: %#v", database)
	}
	if _, ok := root["environments"]; ok {
		t.Fatal("environments section must be removed")
	}

	tenants, _ := maps.GetMap(root, []string{"tenants"})
	acme := tenants["acme"].(map[string]any)
	if acme["domain"] != "acme.production.example" {
		t.Fatalf("environment tenant values were not applied: %#v", acme)
	}
	if database := acme["database"].(map[string]any); database["host"] != "environment-db" {
		t.Fatalf("environment values were not applied to the existing tenant: %#v", database)
	}
	newco := tenants["newco"].(map[string]any)
	if newco["domain"] != "newco.production.example" {
		t.Fatalf("new environment tenant was not created: %#v", newco)
	}
}

func TestMergeRootIntoTenants(t *testing.T) {
	values := document(map[string]any{
		"config": map[string]any{
			"domain": "root",
			"database": map[string]any{
				"host": "root-db",
				"port": 5432,
			},
			"tenants": map[string]any{
				"acme": map[string]any{
					"domain": "acme",
					"database": map[string]any{
						"host": "acme-db",
					},
				},
			},
		},
	})

	if err := MergeRootIntoTenants(values); err != nil {
		t.Fatalf("MergeRootIntoTenants: %v", err)
	}
	if err := SetTenantCodes(values); err != nil {
		t.Fatalf("SetTenantCodes: %v", err)
	}

	root := rootOf(t, values)
	if root["domain"] != "root" {
		t.Fatalf("root was modified: %#v", root)
	}
	tenants, _ := maps.GetMap(root, []string{"tenants"})
	acme := tenants["acme"].(map[string]any)
	want := map[string]any{
		"domain":     "acme",
		"tenantCode": "acme",
		"database":   map[string]any{"host": "acme-db", "port": 5432},
	}
	if !reflect.DeepEqual(acme, want) {
		t.Fatalf("tenant = %#v, want %#v", acme, want)
	}
}

func TestInitialDefaultTenant(t *testing.T) {
	values := Initial("default")
	tenants, _ := maps.GetMap(rootOf(t, values), []string{"tenants"})
	if tenant := tenants["default"].(map[string]any); tenant["tenantCode"] != "default" {
		t.Fatalf("unexpected default tenant: %#v", tenant)
	}

	empty := Initial("")
	if _, ok := maps.GetMap(rootOf(t, empty), []string{"tenants"}); ok {
		t.Fatal("empty default tenant must not create a tenants section")
	}
}

func TestResolveRootAndTenants(t *testing.T) {
	values := document(map[string]any{
		"config": map[string]any{
			"domain":    "root.example",
			"scheme":    "https",
			"publicUrl": "${scheme}://${domain}",
			"static":    "unchanged",
			"domains":   []any{"one.example", "two.example"},
			"allDomains": []any{
				"zero.example",
				"*${domains}",
			},
			"tenants": map[string]any{
				"acme": map[string]any{
					"domain":    "acme.example",
					"publicUrl": "${scheme}://${domain}/acme",
				},
			},
		},
	})
	root := rootOf(t, values)
	// Tenants hold copies of root values, so resolve them first to simulate
	// the merge having happened.
	tenants, _ := maps.ExtractMap(root, []string{"tenants"})
	acme := tenants["acme"].(map[string]any)
	acme["scheme"] = "https"
	root["tenants"] = tenants

	if err := Resolve(context.Background(), values); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if root["publicUrl"] != "https://root.example" {
		t.Fatalf("publicUrl = %v", root["publicUrl"])
	}
	if root["static"] != "unchanged" {
		t.Fatalf("static = %v", root["static"])
	}
	allDomains := root["allDomains"].([]any)
	if len(allDomains) != 3 || allDomains[1] != "one.example" || allDomains[2] != "two.example" {
		t.Fatalf("allDomains = %#v", allDomains)
	}
	tenants, _ = maps.GetMap(root, []string{"tenants"})
	acme = tenants["acme"].(map[string]any)
	if acme["publicUrl"] != "https://acme.example/acme" {
		t.Fatalf("tenant publicUrl = %v", acme["publicUrl"])
	}
}

// referenceMergeRootIntoTenants is the previous two-pass algorithm, kept in
// tests as the semantic reference for the fused implementation.
func referenceMergeRootIntoTenants(document map[string]any) error {
	root, ok := Config(document)
	if !ok {
		return nil
	}
	tenants, ok := maps.GetMap(root, []string{sections.Tenants})
	if !ok {
		return nil
	}
	base := maps.DeepCopyMap(root)
	delete(base, sections.Tenants)
	for code, raw := range tenants {
		tenant := raw.(map[string]any)
		merged := maps.DeepCopyMap(base)
		maps.Merge(merged, tenant)
		tenants[code] = merged
	}
	return nil
}

func TestMergeRootIntoTenantsMatchesReference(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
	}{
		{
			name: "overlapping objects and lists",
			config: map[string]any{
				"domain": "root",
				"list":   []any{"root"},
				"database": map[string]any{
					"host":    "root-db",
					"port":    5432,
					"options": map[string]any{"a": "root", "b": "root"},
				},
				"tenants": map[string]any{
					"acme": map[string]any{
						"domain":   "acme",
						"list":     []any{"acme"},
						"database": map[string]any{"host": "acme-db", "options": map[string]any{"b": "acme", "c": "acme"}},
					},
					"globex": map[string]any{
						"database": "scalar-overrides-object",
					},
				},
			},
		},
		{
			name: "root object replaced by tenant scalar and tenant extra keys",
			config: map[string]any{
				"nested": map[string]any{"x": 1, "y": map[string]any{"z": true}},
				"scalar": "root",
				"tenants": map[string]any{
					"t1": map[string]any{"nested": "flat", "extra": []any{1, 2}},
					"t2": map[string]any{"nested": map[string]any{"y": map[string]any{"w": 0}}},
				},
			},
		},
		{
			name: "nil and empty values",
			config: map[string]any{
				"nilValue":  nil,
				"emptyMap":  map[string]any{},
				"emptyList": []any{},
				"tenants":   map[string]any{"t": map[string]any{"nilValue": "set"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := maps.DeepCopyMap(document(test.config))
			want := maps.DeepCopyMap(document(test.config))

			if err := MergeRootIntoTenants(got); err != nil {
				t.Fatalf("MergeRootIntoTenants: %v", err)
			}
			if err := referenceMergeRootIntoTenants(want); err != nil {
				t.Fatalf("reference: %v", err)
			}
			if !reflect.DeepEqual(rootOf(t, got), rootOf(t, want)) {
				t.Fatalf("merged root = %#v, want %#v", rootOf(t, got), rootOf(t, want))
			}
			if !reflect.DeepEqual(rootOf(t, got)["tenants"], rootOf(t, want)["tenants"]) {
				t.Fatalf("merged tenants = %#v, want %#v", rootOf(t, got)["tenants"], rootOf(t, want)["tenants"])
			}
		})
	}
}
