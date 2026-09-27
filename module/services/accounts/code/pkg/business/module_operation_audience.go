package business

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModuleOperationAudience is immutable deployment policy. A caller selects the
// installed binding and whether it is invoking or only looking up a receipt; it
// never supplies an audience, scope, resource, action, or lifetime.
//
// HeadlessScopes is the separate, optional grant for work no person is present
// for (MintModuleOperationContext). It is never derived from InvokeScopes: a
// binding that declares none cannot be minted headless at all, and one that
// declares some is bounded by InvokeScopes, so the module acting alone can never
// do more with an audience than it could on a person's behalf.
//
// SourceDelegationScopes is the third, equally separate grant: what a datasource
// source's sync may do when a person connected the source and so delegated it
// to this binding (MintSourceOperationContext). Declaring it is what makes the
// binding the one a connect records a delegation for; at most one binding of a
// module may declare it, and like HeadlessScopes it is bounded by InvokeScopes,
// because the context it yields acts on that person's behalf.
type ModuleOperationAudience struct {
	Audience               string                 `json:"audience"`
	InvokeScopes           []ModuleOperationScope `json:"invoke_scopes"`
	LookupScopes           []ModuleOperationScope `json:"lookup_scopes"`
	HeadlessScopes         []ModuleOperationScope `json:"headless_scopes,omitempty"`
	SourceDelegationScopes []ModuleOperationScope `json:"source_delegation_scopes,omitempty"`
}

type ModuleOperationScope struct {
	ResourceKind string   `json:"resource_kind"`
	Actions      []string `json:"actions"`
	ResourceIDs  []string `json:"resource_ids,omitempty"`
}

func (b *ModuleOperationAudience) UnmarshalJSON(raw []byte) error {
	type policy ModuleOperationAudience
	var value policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*b = ModuleOperationAudience(value)
	return nil
}

func validOperationScopes(scopes []ModuleOperationScope, lookup bool) bool {
	if len(scopes) == 0 || len(scopes) > 16 {
		return false
	}
	previousKind := ""
	for _, scope := range scopes {
		if !validOperationValue(scope.ResourceKind, 128) || previousKind >= scope.ResourceKind || len(scope.Actions) == 0 || len(scope.Actions) > 32 || len(scope.ResourceIDs) > 64 {
			return false
		}
		previousKind = scope.ResourceKind
		if !sortedUniqueOperationValues(scope.Actions, 128) || !sortedUniqueOperationValues(scope.ResourceIDs, 512) {
			return false
		}
		for _, action := range scope.Actions {
			if action == "*" || lookup && action != "read" {
				return false
			}
		}
		if slices.Contains(scope.ResourceIDs, "*") {
			return false
		}
	}
	return true
}

func sortedUniqueOperationValues(values []string, limit int) bool {
	for i, value := range values {
		if !validOperationValue(value, limit) || i > 0 && values[i-1] >= value {
			return false
		}
	}
	return true
}

func validOperationValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n\t")
}

func operationScopesSubset(subset, superset []ModuleOperationScope) bool {
	byKind := make(map[string]ModuleOperationScope, len(superset))
	for _, scope := range superset {
		byKind[scope.ResourceKind] = scope
	}
	for _, scope := range subset {
		parent, ok := byKind[scope.ResourceKind]
		if !ok || !operationValuesSubset(scope.Actions, parent.Actions) || len(parent.ResourceIDs) > 0 && (len(scope.ResourceIDs) == 0 || !operationValuesSubset(scope.ResourceIDs, parent.ResourceIDs)) {
			return false
		}
	}
	return true
}

func operationValuesSubset(subset, superset []string) bool {
	for _, value := range subset {
		index, found := slices.BinarySearch(superset, value)
		if !found || index >= len(superset) {
			return false
		}
	}
	return true
}

func validateOperationAudiences(prefix string, bindings map[string]ModuleOperationAudience) error {
	if len(bindings) > 32 {
		return fmt.Errorf("module operation audience bindings exceed limit")
	}
	for id, binding := range bindings {
		if !validOperationValue(id, 128) || !validOperationValue(binding.Audience, 512) || binding.Audience == prefix || !validOperationScopes(binding.InvokeScopes, false) || !validOperationScopes(binding.LookupScopes, true) || !operationScopesSubset(binding.LookupScopes, binding.InvokeScopes) {
			return fmt.Errorf("invalid installed module operation audience binding")
		}
		// Absent is the fail-closed default (no headless mint); present but empty
		// is a misconfiguration rather than a second spelling of absent.
		if binding.HeadlessScopes != nil && (!validOperationScopes(binding.HeadlessScopes, false) || !operationScopesSubset(binding.HeadlessScopes, binding.InvokeScopes)) {
			return fmt.Errorf("invalid installed module operation audience headless scopes")
		}
		// The same two rules for the source-delegation grant: absent is "this
		// binding receives no delegation", present but empty is a mistake.
		if binding.SourceDelegationScopes != nil && (!validOperationScopes(binding.SourceDelegationScopes, false) || !operationScopesSubset(binding.SourceDelegationScopes, binding.InvokeScopes)) {
			return fmt.Errorf("invalid installed module operation audience source delegation scopes")
		}
	}
	// A connect names exactly one binding per module, so two bindings both
	// accepting delegations would leave the host to guess which one a person
	// meant to delegate to. Refuse the declaration instead of choosing.
	delegating := 0
	for _, binding := range bindings {
		if binding.SourceDelegationScopes != nil {
			delegating++
		}
	}
	if delegating > 1 {
		return fmt.Errorf("at most one installed module operation audience binding may declare source delegation scopes")
	}
	return nil
}

func (s *Service) ModuleOperationAudience(caller ModuleCaller, tenant, parentAudience, bindingID string) (ModuleOperationAudience, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return ModuleOperationAudience{}, err
	}
	if err = authorizeTenant(caller, grant, tenant); err != nil {
		return ModuleOperationAudience{}, err
	}
	if grant.Prefix == "" || parentAudience != grant.Prefix {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "incoming module audience mismatch")
	}
	binding, ok := grant.OperationAudiences[bindingID]
	if !ok || validateOperationAudiences(grant.Prefix, map[string]ModuleOperationAudience{bindingID: binding}) != nil {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "installed operation audience required")
	}
	return binding, nil
}

func (b ModuleOperationAudience) WireScopes(lookup bool) []*gen.WorkContextScope {
	scopes := b.InvokeScopes
	if lookup {
		scopes = b.LookupScopes
	}
	return wireOperationScopes(scopes)
}

func wireOperationScopes(scopes []ModuleOperationScope) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &gen.WorkContextScope{ResourceKind: scope.ResourceKind, Actions: append([]string(nil), scope.Actions...), ResourceIds: append([]string(nil), scope.ResourceIDs...)})
	}
	return out
}
