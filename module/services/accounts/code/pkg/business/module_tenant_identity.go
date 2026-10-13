package business

import (
	"context"
	"errors"
	"slices"
	"time"
)

// What a per-tenant module identity would assert, read for an operator.
//
// A composed module acting with no person present mints a Work Context for one
// of its installed operation bindings and names the organization it acts FOR
// (ModuleAuthorizeOperationContext). That capability is the per-tenant module
// identity: its tenant is the organization named, its scopes are that
// organization's installation narrowed to the binding's headless_scopes, it
// lives ModuleOperationContextTTL, and every issuance is recorded.
//
// This answers what that mint WOULD assert, and issues nothing. The mint needs
// the module pod's projected ServiceAccount token held against the build its
// authority document approves, which an operator's browser does not have and
// must never be given a way around: a path that handed an operator a module's
// capability would be a second spelling of that module's identity. So the
// resolution deliberately stops one step short of the signature, and takes the
// same decision from the same declared facts the mint takes it from — not a
// second copy of the rule, because every refusal below is the mint's own
// function answering.

// ModuleTenantIdentity is what one per-tenant mint would assert.
type ModuleTenantIdentity struct {
	PrincipalID string
	Tenant      string
	Audience    string
	// Scopes is what the capability would carry, as `kind:action[:resource]`.
	Scopes   []string
	Lifetime time.Duration
	Revision uint64
	// Shared says the principal is unpinned and declares cross_tenant, so it
	// may act for more than one organization. A pinned principal can only ever
	// name the tenant its envelope declares.
	Shared bool
}

// ModuleTenantIdentityMint is one capability actually issued, from the trail
// that records every mint. There is no other record of one.
type ModuleTenantIdentityMint struct {
	Record   string
	Tenant   string
	Binding  string
	Audience string
	Scopes   []string
	MintedAt time.Time
}

// ModuleTenantIdentityResolution is the answer: either what would be minted, or
// why nothing would be.
type ModuleTenantIdentityResolution struct {
	Identity *ModuleTenantIdentity
	Refusal  string
	Recent   []ModuleTenantIdentityMint
}

// The refusals, in the operator's words rather than as an error code. Each one
// is a decision about declared authority — never an outage to retry.
const (
	// ModuleTenantIdentityUndeclared: no such module principal is derived from
	// the current envelope, so nothing can be minted under that prefix at all.
	ModuleTenantIdentityUndeclared = "no module principal is declared under this prefix"
	// ModuleTenantIdentityTenantRefused: the envelope pins this principal to
	// another tenant, or leaves it unpinned without cross_tenant — either way
	// it may not act for the organization named.
	ModuleTenantIdentityTenantRefused = "this module may not act for that organization: its envelope pins it elsewhere, or does not declare cross_tenant"
	// ModuleTenantIdentityNotInstalled: the organization has no live
	// installation consenting to this module.
	ModuleTenantIdentityNotInstalled = "the organization has no live installation of this module"
	// ModuleTenantIdentityBindingRefused: the binding is not declared, or
	// declares no headless_scopes, or this organization's installation scope
	// leaves it with none — the last being the per-tenant bound doing its job.
	ModuleTenantIdentityBindingRefused = "this binding is not mintable for that organization with no person present: it declares no headless scopes, or the organization's installation consents to none of them"
)

// ResolveModuleTenantIdentity answers what a per-tenant module operation
// context would assert for one module, one of its bindings and one
// organization — or why it would be refused — and the capabilities that module
// was actually issued for that organization.
//
// The decision order is the mint's, and each step answers a different question
// an operator asks: is the principal declared at all, may it act for this
// organization, does this organization install it, and does the binding confer
// anything headless HERE. Collapsing them into one refusal would answer none
// of them.
func (s *Service) ResolveModuleTenantIdentity(
	ctx context.Context, prefix, bindingID, orgID string,
) (ModuleTenantIdentityResolution, error) {
	if prefix == "" || bindingID == "" || orgID == "" {
		return ModuleTenantIdentityResolution{}, errors.New("module prefix, binding and organization are required")
	}
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleTenantIdentityResolution{}, fenceErr
	}
	defer releaseAuthority()

	// The trail is read whatever the answer: an operator looking at a module
	// that may no longer act for an organization still needs to see what it was
	// issued while it could.
	recent, err := s.moduleTenantIdentityMints(ctx, prefix, orgID)
	if err != nil {
		return ModuleTenantIdentityResolution{}, err
	}
	resolution := ModuleTenantIdentityResolution{Recent: recent}

	snapshot := s.moduleRegistrySnapshot()
	principalID := ModulePrincipalID(prefix)
	declared, isModule := snapshot.registry[principalID]
	if !isModule || declared.Prefix == "" {
		resolution.Refusal = ModuleTenantIdentityUndeclared
		return resolution, nil
	}
	// The envelope's own rule about which organizations this principal may
	// name, asked of the function the mint asks (never a second reading of
	// `tenant` and `cross_tenant` here).
	if _, err := moduleMintOrganization(declared, []string{orgID}); err != nil {
		resolution.Refusal = ModuleTenantIdentityTenantRefused
		return resolution, nil
	}
	installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot,
		ModuleCaller{PrincipalID: principalID, BoundOrg: orgID})
	if err != nil {
		// A module the organization does not install is a declared refusal, not
		// a failure of this read; anything else (an unreadable envelope
		// revision, a fence) is the caller's to see as it is.
		if errors.Is(err, ErrModuleInstallationInactive) || errors.Is(err, ErrModuleTenantNotCurrent) {
			resolution.Refusal = ModuleTenantIdentityNotInstalled
			return resolution, nil
		}
		return ModuleTenantIdentityResolution{}, err
	}
	// AFTER the installation scope narrowed it. A binding with headless scopes
	// the deployment declares but this organization did not consent to has none
	// here, and that is the per-tenant bound rather than an edge case.
	grant := installed.Grant
	binding, bound := grant.OperationAudiences[bindingID]
	if !bound || len(binding.HeadlessScopes) == 0 ||
		validateOperationAudiences(grant.Prefix, map[string]ModuleOperationAudience{bindingID: binding}) != nil {
		resolution.Refusal = ModuleTenantIdentityBindingRefused
		return resolution, nil
	}
	resolution.Identity = &ModuleTenantIdentity{
		PrincipalID: principalID,
		Tenant:      orgID,
		Audience:    binding.Audience,
		Scopes:      operationScopeGrants(binding.HeadlessScopes),
		Lifetime:    ModuleOperationContextTTL,
		Revision:    ModuleOperationContextRevision(grant),
		Shared:      declared.Tenant == "" && declared.CrossTenant,
	}
	return resolution, nil
}

// moduleTenantIdentityMints reads the capabilities one module was issued for
// one organization, newest first, from the trail that records every mint.
func (s *Service) moduleTenantIdentityMints(
	ctx context.Context, prefix, orgID string,
) ([]ModuleTenantIdentityMint, error) {
	entries, _, _, err := s.QueryAuditLog(ctx, AuditQuery{
		OrgID:      orgID,
		EventType:  string(EventModuleOperationContextMint),
		Resource:   "module",
		ResourceID: prefix,
		PageSize:   25,
	})
	if err != nil {
		return nil, err
	}
	mints := make([]ModuleTenantIdentityMint, 0, len(entries))
	for _, entry := range entries {
		mint := ModuleTenantIdentityMint{Record: entry.ID, MintedAt: entry.CreatedAt}
		mint.Tenant, _ = entry.Payload["tenant"].(string)
		mint.Binding, _ = entry.Payload["binding_id"].(string)
		mint.Audience, _ = entry.Payload["audience"].(string)
		mint.Scopes = slices.Clip(auditStringList(entry.Payload["scopes"]))
		mints = append(mints, mint)
	}
	return mints, nil
}
