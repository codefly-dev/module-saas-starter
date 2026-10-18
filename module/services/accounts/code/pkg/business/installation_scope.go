package business

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
)

// InstallationScope is consent, never identity. Missing fields grant nothing.
// A nil scope on an INSTALL request means the full current ceiling; the store
// always persists the resulting explicit scope, never a wildcard or a default.
type InstallationScope struct {
	Capabilities       bool                                `json:"capabilities"`
	Queues             []string                            `json:"queues,omitempty"`
	Namespaces         []string                            `json:"namespaces,omitempty"`
	ExternalNamespaces []string                            `json:"external_namespaces,omitempty"`
	Resources          []string                            `json:"resources,omitempty"`
	ReadAudiences      map[string]ModuleReadAudience       `json:"read_audiences,omitempty"`
	OperationAudiences map[string]ModuleOperationAudience  `json:"operation_audiences,omitempty"`
	ArtifactPolicies   map[string]ExecutableArtifactPolicy `json:"artifact_policies,omitempty"`
}

// FullInstallationScope copies the delivered authority already bounded by the
// envelope. No identity, workload, tenancy or cross_tenant field is installable.
func FullInstallationScope(g ModulePrincipalGrant) InstallationScope {
	s := InstallationScope{true, g.Queues, g.Namespaces, g.ExternalNamespaces, g.Resources,
		g.ReadAudiences, g.OperationAudiences, g.ArtifactPolicies}
	raw, _ := json.Marshal(s)
	var out InstallationScope
	_ = json.Unmarshal(raw, &out)
	return out
}

func scopeStringsWithin(a, b []string) bool {
	for _, value := range a {
		if !slices.Contains(b, value) {
			return false
		}
	}
	return true
}

// InstallationScopeWithin is used at install and narrowing. In particular an
// empty resource-id list means all resources, so cannot fit a bounded list.
func InstallationScopeWithin(a, b InstallationScope) bool {
	if a.Capabilities && !b.Capabilities ||
		!scopeStringsWithin(a.Queues, b.Queues) || !scopeStringsWithin(a.Namespaces, b.Namespaces) ||
		!scopeStringsWithin(a.ExternalNamespaces, b.ExternalNamespaces) || !scopeStringsWithin(a.Resources, b.Resources) {
		return false
	}
	for id, binding := range a.OperationAudiences {
		parent, ok := b.OperationAudiences[id]
		if !ok || binding.Audience != parent.Audience ||
			!operationScopesSubset(binding.InvokeScopes, parent.InvokeScopes) ||
			!operationScopesSubset(binding.LookupScopes, parent.LookupScopes) ||
			!operationScopesSubset(binding.HeadlessScopes, parent.HeadlessScopes) ||
			!operationScopesSubset(binding.SourceDelegationScopes, parent.SourceDelegationScopes) {
			return false
		}
	}
	for id, binding := range a.ReadAudiences {
		parent, ok := b.ReadAudiences[id]
		if !ok || binding.Audience != parent.Audience || !operationScopesSubset(readOperationScopes(binding.Scopes), readOperationScopes(parent.Scopes)) {
			return false
		}
	}
	// Artifact policy is an indivisible approval. An installation can remove one,
	// never manufacture or modify the approval's policy, source or destination.
	for id, policy := range a.ArtifactPolicies {
		parent, ok := b.ArtifactPolicies[id]
		if !ok || !reflect.DeepEqual(policy, parent) {
			return false
		}
	}
	return true
}

func readOperationScopes(scopes []ModuleReadScope) []ModuleOperationScope {
	out := make([]ModuleOperationScope, 0, len(scopes))
	for _, scope := range scopes {
		ids := slices.Clone(scope.ResourceIDs)
		slices.Sort(ids)
		out = append(out, ModuleOperationScope{scope.ResourceKind, []string{"read"}, ids})
	}
	return out
}

// intersectInstallationScope never mutates the registry snapshot. Bindings,
// actions AND resource IDs remain bounded even if the envelope shrank after an
// installation was written. Empty intersections remove the binding altogether.
func intersectInstallationScope(g ModulePrincipalGrant, consent InstallationScope) ModulePrincipalGrant {
	out := g
	out.Queues = keepDeclared(g.Queues, consent.Queues)
	out.Namespaces = keepDeclared(g.Namespaces, consent.Namespaces)
	out.ExternalNamespaces = keepDeclared(g.ExternalNamespaces, consent.ExternalNamespaces)
	out.Resources = keepDeclared(g.Resources, consent.Resources)
	out.OperationAudiences = nil
	if g.OperationAudiences != nil {
		out.OperationAudiences = map[string]ModuleOperationAudience{}
	}
	for id, binding := range g.OperationAudiences {
		installed, ok := consent.OperationAudiences[id]
		if !ok || installed.Audience != binding.Audience {
			continue
		}
		// A binding granted headless-only stays headless-only: it survives on
		// its consented headless scopes. One granted invoke survives only on
		// invoke, as before — consent never turns it into a headless-only one.
		headlessOnly := binding.headlessOnly()
		binding.InvokeScopes = nilIfEmpty(intersectOperationScopes(binding.InvokeScopes, installed.InvokeScopes))
		binding.LookupScopes = nilIfEmpty(intersectOperationScopes(binding.LookupScopes, installed.LookupScopes))
		binding.HeadlessScopes = nilIfEmpty(intersectOperationScopes(binding.HeadlessScopes, installed.HeadlessScopes))
		binding.SourceDelegationScopes = nilIfEmpty(intersectOperationScopes(binding.SourceDelegationScopes, installed.SourceDelegationScopes))
		if len(binding.InvokeScopes) > 0 || headlessOnly && binding.HeadlessScopes != nil {
			out.OperationAudiences[id] = binding
		}
	}
	out.ReadAudiences = nil
	if g.ReadAudiences != nil {
		out.ReadAudiences = map[string]ModuleReadAudience{}
	}
	for id, binding := range g.ReadAudiences {
		installed, ok := consent.ReadAudiences[id]
		if !ok || installed.Audience != binding.Audience {
			continue
		}
		scopes := intersectOperationScopes(readOperationScopes(binding.Scopes), readOperationScopes(installed.Scopes))
		binding.Scopes = nil
		for _, scope := range scopes {
			binding.Scopes = append(binding.Scopes, ModuleReadScope{scope.ResourceKind, scope.ResourceIDs})
		}
		if len(binding.Scopes) > 0 {
			out.ReadAudiences[id] = binding
		}
	}
	out.ArtifactPolicies = nil
	if g.ArtifactPolicies != nil {
		out.ArtifactPolicies = map[string]ExecutableArtifactPolicy{}
	}
	for id, policy := range g.ArtifactPolicies {
		if installed, ok := consent.ArtifactPolicies[id]; ok && reflect.DeepEqual(installed, policy) {
			out.ArtifactPolicies[id] = policy
		}
	}
	return out
}

func validateInstallationScope(scope InstallationScope, grant ModulePrincipalGrant) error {
	if !InstallationScopeWithin(scope, FullInstallationScope(grant)) {
		return fmt.Errorf("installation scope exceeds the current module ceiling")
	}
	for _, binding := range scope.OperationAudiences {
		for _, scopes := range [][]ModuleOperationScope{binding.InvokeScopes, binding.LookupScopes, binding.HeadlessScopes, binding.SourceDelegationScopes} {
			if len(scopes) > 0 && !validOperationScopes(scopes, false) {
				return fmt.Errorf("installation operation scopes must be canonical")
			}
		}
	}
	return nil
}

func moduleAvailableToOrganization(g ModulePrincipalGrant, orgID string) bool {
	return orgID != "" && (g.CrossTenant || g.Tenant == "" || g.Tenant == orgID)
}

// ParseInstallationScope accepts the same JSON scope shape stored by accounts.
// Unknown fields are errors: misspelled consent must never silently grant more.
func ParseInstallationScope(document string) (InstallationScope, error) {
	var scope InstallationScope
	raw := bytes.TrimSpace([]byte(document))
	if len(raw) == 0 || raw[0] != '{' {
		return scope, fmt.Errorf("installation scope must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return scope, fmt.Errorf("invalid installation scope: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return scope, fmt.Errorf("installation scope must contain exactly one JSON object")
	}
	return scope, nil
}
