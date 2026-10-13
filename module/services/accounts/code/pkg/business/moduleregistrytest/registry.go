// Package moduleregistrytest states a module principal registry for a test.
//
// No production path reads a registry from text: the running registry is
// derived each reconcile pass from delivered authority inside the module
// envelope (business.ModulePrincipalReconciler). A capability test still needs
// a registry in hand, and the shortest honest way to write one is the entry's
// own shape — so this parses `{<prefix>: {<grant>, "workload": {...}}}` and
// holds every entry to business.ValidateModulePrincipalGrant, the same check
// the reconciler runs over each entry it derives. A fixture the reconciler
// could never install fails here instead of testing a state no host reaches.
package moduleregistrytest

import (
	"encoding/json"
	"fmt"

	"accounts/pkg/business"

	"github.com/google/uuid"
)

type entry struct {
	ArtifactPolicies   map[string]business.ExecutableArtifactPolicy `json:"artifact_policies"`
	ReadAudiences      map[string]business.ModuleReadAudience       `json:"read_audiences"`
	OperationAudiences map[string]business.ModuleOperationAudience  `json:"operation_audiences"`
	Queues             []string                                     `json:"queues"`
	Namespaces         []string                                     `json:"namespaces"`
	ExternalNamespaces []string                                     `json:"external_namespaces"`
	Resources          []string                                     `json:"resources"`
	CrossTenant        bool                                         `json:"cross_tenant"`
	Tenant             string                                       `json:"tenant"`
	Workload           business.ModuleWorkload                      `json:"workload"`
}

// Parse decodes a registry keyed by module prefix and validates every entry.
// An empty string is an empty registry.
func Parse(raw string) (business.ModulePrincipalRegistry, error) {
	registry := business.ModulePrincipalRegistry{}
	if raw == "" {
		return registry, nil
	}
	var wire map[string]entry
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	for prefix, declared := range wire {
		// The tenant is canonicalized as the envelope parser canonicalizes it,
		// so a fixture spelling it in upper case states what a host would hold.
		tenant := ""
		if declared.Tenant != "" {
			parsed, err := uuid.Parse(declared.Tenant)
			if err != nil {
				return nil, fmt.Errorf("module principal %q must declare its tenant as an organization id: %w", prefix, err)
			}
			tenant = parsed.String()
		}
		grant := business.ModulePrincipalGrant{
			ArtifactPolicies:   declared.ArtifactPolicies,
			ReadAudiences:      declared.ReadAudiences,
			OperationAudiences: declared.OperationAudiences,
			Prefix:             prefix,
			Queues:             declared.Queues,
			Namespaces:         declared.Namespaces,
			ExternalNamespaces: declared.ExternalNamespaces,
			Resources:          declared.Resources,
			CrossTenant:        declared.CrossTenant,
			Tenant:             tenant,
			Workload:           declared.Workload,
		}
		if err := business.ValidateModulePrincipalGrant(prefix, grant); err != nil {
			return nil, err
		}
		registry[business.ModulePrincipalID(prefix)] = grant
	}
	return registry, nil
}
