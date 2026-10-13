package business

import (
	"encoding/json"

	"github.com/google/uuid"
)

// parseRegistryFixture states a module principal registry for a test in this
// package, which cannot import moduleregistrytest (it imports this package).
// Same shape and same check: the envelope's grant plus a workload, every entry
// held to ValidateModulePrincipalGrant — what the reconciler runs over each
// entry it derives.
func parseRegistryFixture(raw string) (ModulePrincipalRegistry, error) {
	registry := ModulePrincipalRegistry{}
	if raw == "" {
		return registry, nil
	}
	var wire map[string]struct {
		moduleEnvelopeGrantWire
		Workload ModuleWorkload `json:"workload"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	for prefix, declared := range wire {
		grant := declared.grant(prefix)
		if tenant, err := uuid.Parse(grant.Tenant); err == nil {
			grant.Tenant = tenant.String()
		}
		grant.Workload = declared.Workload
		if err := ValidateModulePrincipalGrant(prefix, grant); err != nil {
			return nil, err
		}
		registry[ModulePrincipalID(prefix)] = grant
	}
	return registry, nil
}
