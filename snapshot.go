package fastconf

import (
	"slices"
	"strings"
	"sync"

	"github.com/edanko/fastconf/internal/maps"
	"github.com/edanko/fastconf/internal/sections"
	"github.com/edanko/fastconf/internal/value"
)

// snapshot is an immutable loaded configuration.
type snapshot struct {
	document    map[string]any
	config      map[string]any
	tenants     map[string]map[string]any
	tenantCodes []string

	treeOnce     sync.Once
	rootValue    value.Value
	tenantValues map[string]*value.Object
}

func newSnapshot(document map[string]any) *snapshot {
	config, _ := maps.GetMap(document, []string{sections.Root})
	if config == nil {
		config = map[string]any{}
	}
	tenants := make(map[string]map[string]any)
	codes := make([]string, 0)
	if rawTenants, ok := config[sections.Tenants].(map[string]any); ok {
		// The tenant count is known here: pre-sizing avoids repeatedly
		// reallocating the map buckets and the code slice while filling them.
		tenants = make(map[string]map[string]any, len(rawTenants))
		codes = make([]string, 0, len(rawTenants))
		for code, tenant := range rawTenants {
			tenantValues, ok := tenant.(map[string]any)
			if !ok {
				continue
			}
			tenants[code] = tenantValues
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	return &snapshot{
		document:    document,
		config:      config,
		tenants:     tenants,
		tenantCodes: codes,
	}
}

func (s *snapshot) ensureTree() {
	s.treeOnce.Do(func() {
		s.rootValue = value.From(s.config)
		s.tenantValues = make(map[string]*value.Object, len(s.tenants))
		for code, tenant := range s.tenants {
			s.tenantValues[code] = value.FromMap(tenant)
		}
	})
}

func (s *snapshot) tenant(code string) (*value.Object, bool) {
	s.ensureTree()
	if tenant, ok := s.tenantValues[code]; ok {
		return tenant, true
	}
	for candidate, tenant := range s.tenantValues {
		if strings.EqualFold(candidate, code) {
			return tenant, true
		}
	}
	return nil, false
}

func (s *snapshot) tenantMap(code string) (map[string]any, bool) {
	if tenant, ok := s.tenants[code]; ok {
		return tenant, true
	}
	for candidate, tenant := range s.tenants {
		if strings.EqualFold(candidate, code) {
			return tenant, true
		}
	}
	return nil, false
}
