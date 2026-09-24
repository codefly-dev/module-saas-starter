package business

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ModuleOperationContextTTL bounds a headless operation context. It is the same
// ceiling an exchanged operation context carries (ExchangeDelegatedOperationAudience
// caps at 60s): the capability addresses another module's service with real
// scopes, so a leaked one must be worthless within a minute. Background work
// mints one per call, or per short batch, rather than holding one open.
const ModuleOperationContextTTL = 60 * time.Second

// ErrModuleOperationContextRefused reports that an authenticated module named a
// binding it may not mint headless: the binding is not declared, or it declares
// no headless_scopes. It is distinct from ErrModuleRegistrationDenied, which is
// a failure to prove the module's identity at all.
var ErrModuleOperationContextRefused = errors.New("module operation binding is not mintable without a person present")

// ModuleOperationContextAuthority is what one headless operation mint asserts:
// the module identity (principal and declared tenant), the binding selected, and
// exactly the audience and scopes that binding declares for headless use.
type ModuleOperationContextAuthority struct {
	ModuleWorkContextAuthority
	BindingID string
	Audience  string
	Scopes    []ModuleOperationScope
	// Revision is the authorization revision the capability is sealed with:
	// ModuleOperationContextRevision of the module's declared grant at mint.
	Revision uint64
}

// WireScopes projects the binding's headless scopes onto the signing surface.
func (a ModuleOperationContextAuthority) WireScopes() []*gen.WorkContextScope {
	return wireOperationScopes(a.Scopes)
}

// ModuleAuthorizeOperationContext resolves the capability a composed module may
// be issued, with no person present, for one of its installed operation
// audiences.
//
// The module authenticates exactly as for its own Work Context — the identity
// secret for its prefix — so nothing new is trusted. The caller then names only
// the binding; audience and scopes are deployment policy. The scopes are the
// binding's headless_scopes and nothing else: invoke_scopes describe what the
// module may do on a person's behalf and are never reused for work that has no
// person, so a binding that declares no headless_scopes is refused (fail closed).
func (s *Service) ModuleAuthorizeOperationContext(prefix, secret, bindingID string) (ModuleOperationContextAuthority, error) {
	identity, err := s.ModuleAuthorizeWorkContext(prefix, secret)
	if err != nil {
		return ModuleOperationContextAuthority{}, err
	}
	grant, registered := s.modulePrincipals[identity.PrincipalID]
	if !registered {
		return ModuleOperationContextAuthority{}, ErrModuleRegistrationDenied
	}
	binding, declared := grant.OperationAudiences[bindingID]
	if !declared || len(binding.HeadlessScopes) == 0 ||
		validateOperationAudiences(grant.Prefix, map[string]ModuleOperationAudience{bindingID: binding}) != nil {
		return ModuleOperationContextAuthority{}, ErrModuleOperationContextRefused
	}
	scopes := make([]ModuleOperationScope, 0, len(binding.HeadlessScopes))
	for _, scope := range binding.HeadlessScopes {
		scopes = append(scopes, ModuleOperationScope{
			ResourceKind: scope.ResourceKind,
			Actions:      slices.Clone(scope.Actions),
			ResourceIDs:  slices.Clone(scope.ResourceIDs),
		})
	}
	return ModuleOperationContextAuthority{
		ModuleWorkContextAuthority: identity,
		BindingID:                  bindingID,
		Audience:                   binding.Audience,
		Scopes:                     scopes,
		Revision:                   ModuleOperationContextRevision(grant),
	}, nil
}

// RecordModuleOperationContextMint commits the durable record of an issued
// headless operation context. Like RecordModuleWorkContextMint it is written
// once the capability exists, and the caller withholds the capability when the
// record cannot be committed. The record names the binding, the audience and
// every scope granted, so an operator can see exactly what the module could do
// without asking the token.
func (s *Service) RecordModuleOperationContextMint(
	ctx context.Context, prefix string, authority ModuleOperationContextAuthority,
) error {
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, authority.PrincipalID, "system", EventModuleOperationContextMint, "module", prefix,
			authority.Tenant, map[string]any{
				"prefix":     prefix,
				"tenant":     authority.Tenant,
				"binding_id": authority.BindingID,
				"audience":   authority.Audience,
				"scopes":     operationScopeGrants(authority.Scopes),
			})
	})
}

// operationScopeGrants flattens scopes to one "kind:action" or
// "kind:action:resource" entry per grant, the shape the audit payload declares.
func operationScopeGrants(scopes []ModuleOperationScope) []string {
	grants := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		for _, action := range scope.Actions {
			if len(scope.ResourceIDs) == 0 {
				grants = append(grants, scope.ResourceKind+":"+action)
				continue
			}
			for _, resourceID := range scope.ResourceIDs {
				grants = append(grants, strings.Join([]string{scope.ResourceKind, action, resourceID}, ":"))
			}
		}
	}
	return grants
}

// ModuleOperationContextRevision is the authorization revision a module
// operation context is sealed with. A module principal is declared by the
// deployment, not registered per organization, so it has no row-backed revision
// counter; its authority IS its MODULE_PRINCIPALS entry. The revision is
// therefore a digest of that entry: deterministic across replicas and restarts,
// and different the moment the entry changes, so a narrowed, widened or removed
// grant stops confirming every context minted under the old one. It is never
// zero, which the revision check's contract reserves for "no revision".
func ModuleOperationContextRevision(grant ModulePrincipalGrant) uint64 {
	// encoding/json writes struct fields in declaration order and map keys
	// sorted, so the same grant always encodes to the same bytes.
	encoded, err := json.Marshal(grant)
	if err != nil {
		// A grant is plain strings, slices and maps; it cannot fail to encode.
		panic("module operation context: grant does not encode: " + err.Error())
	}
	sum := sha256.Sum256(append([]byte("codefly.saas.module-operation-context.v1\x00"), encoded...))
	revision := binary.BigEndian.Uint64(sum[:8]) & (1<<63 - 1)
	if revision == 0 {
		return 1
	}
	return revision
}

// ModuleOperationRevisionSubject is one subject of a revision check against a
// module operation context: a principal and the scopes the context grants it.
type ModuleOperationRevisionSubject struct {
	PrincipalID string
	Scopes      []ModuleOperationScope
}

// ErrModuleOperationContextStale reports that a revision check presented a
// context owned by a declared module principal that the module's current
// declaration no longer confirms. It is a denial, never an outage.
var ErrModuleOperationContextStale = errors.New("module operation context is not confirmed by the current module declaration")

// CheckModuleOperationContextRevision confirms a Work Context whose owner is a
// declared module's service principal — the only such contexts are those
// MintModuleOperationContext issues. It reports handled=false when the owner is
// not a module principal, so the caller falls through to the row-backed check
// every person- and installation-owned context takes.
//
// For a module owner the answer comes from the declaration alone, and every
// mismatch is ErrModuleOperationContextStale, never a widening:
//   - the tenant must be the one the module principal declares (the mint never
//     seals another);
//   - the revision must equal ModuleOperationContextRevision of the current
//     entry, so any change to the entry revokes outstanding contexts;
//   - every subject must be the module principal itself (its owner position and
//     its sole actor hop);
//   - every subject's scopes must lie within the headless_scopes of ONE of the
//     module's operation bindings — the same subset rule the mint applied.
func (s *Service) CheckModuleOperationContextRevision(
	orgID, ownerPrincipalID string, revision uint64, subjects []ModuleOperationRevisionSubject,
) (handled bool, err error) {
	grant, isModule := s.modulePrincipals[ownerPrincipalID]
	if !isModule {
		return false, nil
	}
	if orgID != grant.Tenant || revision != ModuleOperationContextRevision(grant) || len(subjects) == 0 {
		return true, ErrModuleOperationContextStale
	}
	for _, subject := range subjects {
		if subject.PrincipalID != ownerPrincipalID {
			return true, ErrModuleOperationContextStale
		}
	}
	for _, binding := range grant.OperationAudiences {
		if len(binding.HeadlessScopes) == 0 {
			continue
		}
		within := true
		for _, subject := range subjects {
			if !operationScopesSubset(subject.Scopes, binding.HeadlessScopes) {
				within = false
				break
			}
		}
		if within {
			return true, nil
		}
	}
	return true, ErrModuleOperationContextStale
}
