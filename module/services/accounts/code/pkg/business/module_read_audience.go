package business

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModuleReadAudience is deployment-owned policy, not exchange request data.
// Incoming audience is the declaring module prefix. All actions are read-only.
type ModuleReadAudience struct {
	Audience string            `json:"audience"`
	Scopes   []ModuleReadScope `json:"scopes"`
}
type ModuleReadScope struct {
	ResourceKind string   `json:"resource_kind"`
	ResourceIDs  []string `json:"resource_ids,omitempty"`
}

// UnmarshalJSON rejects misspelled or caller-style scope/action configuration.
func (b *ModuleReadAudience) UnmarshalJSON(raw []byte) error {
	type policy ModuleReadAudience
	var value policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*b = ModuleReadAudience(value)
	return nil
}

func validateReadAudiences(prefix string, bindings map[string]ModuleReadAudience) error {
	if len(bindings) > 16 {
		return fmt.Errorf("module read audience bindings exceed limit")
	}
	for id, binding := range bindings {
		if id == "" || len(id) > 128 || strings.TrimSpace(binding.Audience) == "" || len(binding.Audience) > 512 || binding.Audience == prefix || len(binding.Scopes) == 0 || len(binding.Scopes) > 16 {
			return fmt.Errorf("invalid installed module read audience binding")
		}
		seen := map[string]bool{}
		for _, scope := range binding.Scopes {
			if strings.TrimSpace(scope.ResourceKind) == "" || len(scope.ResourceKind) > 128 || len(scope.ResourceIDs) > 64 || seen[scope.ResourceKind] {
				return fmt.Errorf("invalid installed module read scope")
			}
			seen[scope.ResourceKind] = true
			ids := map[string]bool{}
			for _, resource := range scope.ResourceIDs {
				if resource == "" || len(resource) > 512 || ids[resource] {
					return fmt.Errorf("invalid installed module read resource")
				}
				ids[resource] = true
			}
		}
	}
	return nil
}

func (s *Service) ModuleReadAudience(caller ModuleCaller, tenant, parentAudience, bindingID string) (ModuleReadAudience, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return ModuleReadAudience{}, err
	}
	if err = authorizeTenant(caller, grant, tenant); err != nil {
		return ModuleReadAudience{}, err
	}
	// Even a cross-tenant capability cannot make a request for another audience.
	if grant.Prefix == "" || parentAudience != grant.Prefix {
		return ModuleReadAudience{}, status.Error(codes.PermissionDenied, "incoming module audience mismatch")
	}
	binding, ok := grant.ReadAudiences[bindingID]
	if !ok || validateReadAudiences(grant.Prefix, map[string]ModuleReadAudience{bindingID: binding}) != nil {
		return ModuleReadAudience{}, status.Error(codes.PermissionDenied, "installed read audience required")
	}
	return binding, nil
}
func (b ModuleReadAudience) WireScopes() []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(b.Scopes))
	for _, scope := range b.Scopes {
		out = append(out, &gen.WorkContextScope{ResourceKind: scope.ResourceKind, ResourceIds: append([]string(nil), scope.ResourceIDs...), Actions: []string{"read"}})
	}
	return out
}
