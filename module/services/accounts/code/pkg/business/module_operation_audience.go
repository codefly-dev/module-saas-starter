package business

import (
	"bytes"
	"context"
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
// declares some beside InvokeScopes is bounded by them, so the module acting
// alone can never do more with an audience than it could on a person's behalf.
//
// A binding may also be HEADLESS-ONLY: no InvokeScopes at all, only
// HeadlessScopes (issue #1156). That is background work no person ever asks
// for — recording evidence, exporting spans, embedding — so it holds no
// person-invocable authority, and it has neither LookupScopes nor
// SourceDelegationScopes, both of which act on a person's behalf and are bounded
// by the invoke grant it does not have. Every person-delegated exchange refuses
// it by name (errOperationAudienceHeadlessOnly); only the headless mint uses it.
//
// SourceDelegationScopes is the third, equally separate grant: what a datasource
// source's sync may do when a person connected the source and so delegated it
// to this binding (MintSourceOperationContext). Declaring it is what makes the
// binding the one a connect records a delegation for; at most one binding of a
// module may declare it, and like HeadlessScopes it is bounded by InvokeScopes,
// because the context it yields acts on that person's behalf.
const DelegationModeContentRead = "content_read"

// DelegationModeModuleActor issues the person's child with the exchanging
// module as its one actor hop, so the callee sees who acts for the person.
const DelegationModeModuleActor = "module_actor"

// DelegationMode is host-owned: content_read requires an authentic installed
// module child and live personal visibility; module_actor requires the same
// installed child for any invoke scope. Empty preserves ordinary exchanges.
type ModuleOperationAudience struct {
	DelegationMode         string                 `json:"delegation_mode,omitempty"`
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

// selfAdmittingBinding reports whether a binding exists for work with NO
// PERSON PRESENT — it declares headless scopes, delegation scopes, or both.
//
// It is the one case in which a binding may name its own module as the
// audience, and the reason is structural rather than a relaxation. Every other
// operation binding describes calling ANOTHER service, so a module naming
// itself would be minting a capability addressed to nobody new, and the
// declaration is refused. Unattended work inverts that: a release's trigger
// declares whose authority an occurrence runs under, and the capability is
// admitted by the module that executes the release — itself. The module
// attaches it as its own incoming execution context and verifies it against
// its own admission audience, which is its declared name. So a composition
// that could not name its own prefix here would have to configure its
// executing module to admit a second audience it does not accept, and neither
// arm of a declared trigger would have anywhere to mint at all.
//
// The guard it narrows is not the one that protects this host's identity
// surface: a capability addressed to "module-capabilities" is refused at
// signing by requireMintableAudience, whatever a binding declares, so self
// admission can never become a second spelling of a module identity.
func selfAdmittingBinding(binding ModuleOperationAudience) bool {
	return binding.HeadlessScopes != nil || binding.SourceDelegationScopes != nil
}

// headlessOnly reports whether a binding grants nothing on a person's behalf:
// no invoke scopes, only headless ones. validateOperationAudiences is what holds
// such a binding to having no lookup and no source-delegation scopes either.
func (b ModuleOperationAudience) headlessOnly() bool {
	return b.InvokeScopes == nil && b.HeadlessScopes != nil
}

// errOperationAudienceHeadlessOnly is the refusal every person-delegated
// exchange gives a headless-only binding: it was granted no invoke, so there is
// nothing it may do as the asking person, and saying so beats signing a
// capability that carries no scope.
func errOperationAudienceHeadlessOnly(bindingID string) error {
	return status.Errorf(codes.PermissionDenied,
		"operation audience binding %q is headless-only: it was granted no invoke scope, so it never acts on a person's behalf", bindingID)
}

func validateOperationAudiences(prefix string, bindings map[string]ModuleOperationAudience) error {
	if len(bindings) > 32 {
		return fmt.Errorf("module operation audience bindings exceed limit")
	}
	for id, binding := range bindings {
		if !validOperationValue(id, 128) || !validOperationValue(binding.Audience, 512) ||
			(binding.Audience == prefix && !selfAdmittingBinding(binding)) {
			return fmt.Errorf("invalid installed module operation audience binding")
		}
		// The module capability surface's own audience is reserved: a context
		// addressed to it is read as a module identity, so no binding may name it.
		if binding.Audience == ModuleCapabilitiesAudience {
			return fmt.Errorf("invalid installed module operation audience binding %q: %q is this host's module capability surface and is never a binding's audience", id, ModuleCapabilitiesAudience)
		}
		if binding.DelegationMode != "" {
			if _, known := installedBindingPrefixes[binding.DelegationMode]; !known || len(binding.InvokeScopes) == 0 || binding.HeadlessScopes != nil || binding.SourceDelegationScopes != nil {
				return fmt.Errorf("invalid installed delegation mode")
			}
			for _, scope := range binding.InvokeScopes {
				if binding.DelegationMode != DelegationModeContentRead {
					break
				}
				if len(scope.Actions) != 1 || scope.Actions[0] != "read" {
					return fmt.Errorf("content-read delegation grants only read")
				}
			}
		}
		// HEADLESS-ONLY: absent invoke is whole only beside headless scopes, and
		// then it carries nothing a person's exchange could redeem — no lookup and
		// no source delegation, both of which act as that person and so are
		// bounded by an invoke grant this binding does not have. An empty invoke
		// list stays a misconfiguration rather than a second spelling of absent.
		if binding.InvokeScopes == nil {
			if binding.HeadlessScopes == nil {
				return fmt.Errorf("invalid installed module operation audience binding: it grants neither invoke nor headless scopes")
			}
			if !validOperationScopes(binding.HeadlessScopes, false) ||
				binding.LookupScopes != nil || binding.SourceDelegationScopes != nil {
				return fmt.Errorf("invalid installed module operation audience binding: one with no invoke scopes is headless-only and declares only headless scopes")
			}
			continue
		}
		if !validOperationScopes(binding.InvokeScopes, false) {
			return fmt.Errorf("invalid installed module operation audience binding")
		}
		// Lookup is OPTIONAL, like headless: a binding that declares only invoke
		// redeems no lookup, and that is a whole declaration — a module verifying
		// citations as the asking person has nothing to look up. Absent is "this
		// binding redeems no lookup" and a lookup exchange under it is refused;
		// present but empty is a misconfiguration, not a second spelling of absent.
		// Requiring it dropped every invoke-only binding a module declared, and the
		// binding then read as never declared at all.
		if binding.LookupScopes != nil && (!validOperationScopes(binding.LookupScopes, true) || !operationScopesSubset(binding.LookupScopes, binding.InvokeScopes)) {
			return fmt.Errorf("invalid installed module operation audience lookup scopes")
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

// ctx is a parameter because this path RE-READS live authority, and a function
// that took none could not. It had none, which is why it was one of the paths
// deciding on the declared ceiling alone.
//
// It resolves the binding a person-delegated exchange redeems, so it — and
// ModuleOperationAudienceForDelegation — refuses a headless-only binding by
// name: nothing that binding was granted may be done as the asking person.
func (s *Service) ModuleOperationAudience(ctx context.Context, caller ModuleCaller, tenant, parentAudience, bindingID string) (ModuleOperationAudience, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleOperationAudience{}, fenceErr
	}
	defer releaseAuthority()

	grant, err := s.moduleCapability(ctx, caller)
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
	if binding.headlessOnly() {
		return ModuleOperationAudience{}, errOperationAudienceHeadlessOnly(bindingID)
	}
	return binding, nil
}

// ModuleOperationAudienceForDelegation resolves the caller's installed binding
// after its parent delegation was confirmed. The delegation and caller must
// belong to the same organization; neither delegation nor cross_tenant bypasses
// that organization's installation and the current envelope.
func (s *Service) ModuleOperationAudienceForDelegation(ctx context.Context, caller ModuleCaller, delegation *SourceDelegation, parentAudience, bindingID string) (ModuleOperationAudience, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleOperationAudience{}, fenceErr
	}
	defer releaseAuthority()

	if !delegation.Active() {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "source delegation is not active")
	}
	if delegation.OrgID != caller.BoundOrg {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "delegation belongs to a different installation organization")
	}
	grant, err := s.moduleCapability(ctx, caller)
	if err != nil {
		return ModuleOperationAudience{}, err
	}
	if grant.Prefix == "" || parentAudience != grant.Prefix {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "incoming module audience mismatch")
	}
	binding, ok := grant.OperationAudiences[bindingID]
	if !ok || validateOperationAudiences(grant.Prefix, map[string]ModuleOperationAudience{bindingID: binding}) != nil {
		return ModuleOperationAudience{}, status.Error(codes.PermissionDenied, "installed operation audience required")
	}
	if binding.headlessOnly() {
		return ModuleOperationAudience{}, errOperationAudienceHeadlessOnly(bindingID)
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

// intersectOperationScopes returns what both scope sets allow, in the canonical
// shape validOperationScopes requires: kinds sorted and unique, actions and
// resource ids sorted and unique, and a kind dropped entirely when nothing of
// it survives rather than kept with an empty action list.
//
// It reads an empty ResourceIDs exactly as operationScopesSubset does — as "any
// resource of this kind" — so an unrestricted scope intersected with a listed
// one yields the list, and two listed ones yield only the ids in both. A kind
// present in one set and not the other contributes nothing.
//
// The result is a subset of both inputs by construction, which is what makes it
// safe to seal into a capability: it can only ever narrow.
func intersectOperationScopes(left, right []ModuleOperationScope) []ModuleOperationScope {
	byKind := make(map[string]ModuleOperationScope, len(right))
	for _, scope := range right {
		byKind[scope.ResourceKind] = scope
	}
	out := make([]ModuleOperationScope, 0, len(left))
	for _, scope := range left {
		other, ok := byKind[scope.ResourceKind]
		if !ok {
			continue
		}
		actions := intersectOperationValues(scope.Actions, other.Actions)
		if len(actions) == 0 {
			continue
		}
		var ids []string
		switch {
		case len(scope.ResourceIDs) == 0:
			ids = slices.Clone(other.ResourceIDs)
		case len(other.ResourceIDs) == 0:
			ids = slices.Clone(scope.ResourceIDs)
		default:
			// Both name resources, so only those in both survive. No overlap is
			// no authority over that kind at all, not authority over all of it.
			if ids = intersectOperationValues(scope.ResourceIDs, other.ResourceIDs); len(ids) == 0 {
				continue
			}
		}
		out = append(out, ModuleOperationScope{ResourceKind: scope.ResourceKind, Actions: actions, ResourceIDs: ids})
	}
	return out
}

func intersectOperationValues(left, right []string) []string {
	out := make([]string, 0, min(len(left), len(right)))
	for _, value := range left {
		if _, found := slices.BinarySearch(right, value); found {
			out = append(out, value)
		}
	}
	return out
}
