// Command tenants demonstrates tenant configuration decoding and placeholders.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

type Tenant struct {
	TenantCode string `mapstructure:"tenantCode"`
	Domain     string `mapstructure:"domain"`
	PublicURL  string `mapstructure:"publicUrl"`
}

func main() {
	loader, err := fastconf.New(
		fastconf.WithDefaultTenant("acme"),
		fastconf.WithSources(layers.NewInMemory(map[string]any{
			"config": map[string]any{
				"scheme": "https",
				"tenants": map[string]any{
					"acme": map[string]any{
						"domain":    "acme.example.com",
						"publicUrl": "${scheme}://${domain}",
					},
					"globex": map[string]any{
						"domain":    "globex.example.com",
						"publicUrl": "${scheme}://${domain}",
					},
				},
			},
		})),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = loader.Close() }()

	codes, err := loader.TenantCodes(context.Background())
	if err != nil {
		log.Print(err)
		return
	}
	for _, code := range codes {
		var tenant Tenant
		if err := loader.DecodeTenant(context.Background(), code, &tenant); err != nil {
			log.Print(err)
			return
		}
		fmt.Printf("%s -> %s\n", tenant.TenantCode, tenant.PublicURL)
	}
}
