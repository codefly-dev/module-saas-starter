package business

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
)

// Module identity: how a composed module proves which module it is, once per
// execution, before it holds any Work Context.
//
// It presents the ServiceAccount token Kubernetes projects into its pod for the
// `accounts` audience, and BindExecution establishes the rest independently of
// anything the caller says: the token is reviewed (TokenReview), the pod it
// names is read, and the image that pod is RUNNING is held against the build the
// principal's authority document approves. A pod on an unapproved build, a token
// for another ServiceAccount or namespace than the principal declares, and a
// token minted for another audience are each issued nothing.
//
// WHAT THIS REPLACED. Every module mint authenticated with a static secret
// (MODULE_IDENTITY_SECRETS), and two more secrets minted gateway registration
// credentials for modules and solutions. A secret is a bearer: whoever holds it
// is the module, so the mint could not answer what the caller was running at
// all, and a copied secret outlived every build it was issued to. All three, the
// parsing, the configuration reads and both registration mints are deleted
// (#1056); there is no secret path left to fall back to.

// ErrModuleIdentityDenied is returned for every refusal to prove a module's
// identity — an unknown or malformed prefix, a token Kubernetes refuses, a
// token for another workload, a pod running an unapproved build — so a caller
// probing the mint cannot tell which check it failed. The cause is wrapped for
// the host's own log and tests, never sent to the caller.
//
// A host that could not ESTABLISH the execution (the API server unreachable, no
// reviewer wired, a container with no image id yet) answers ErrExecutionUnbound
// instead, which is not a verdict and is retryable.
var ErrModuleIdentityDenied = errors.New("module identity denied")

// registrationIdentityPattern is the module-prefix rule: one lowercase DNS-ish
// segment, no slashes, wildcards, or traversal — the gateway's catalog-identity
// rule. (The name predates the deletion of runtime registration; every reader
// of it is a prefix check.)
var registrationIdentityPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// ModuleWorkContextAuthority is the identity one module Work Context mint
// asserts: who the module acts as, on which tenant, and the execution the host
// bound that identity to. The capabilities that identity may exercise are not
// sealed here — every call re-reads the declared grant, so narrowing a module's
// authority takes effect immediately rather than when its current token expires.
type ModuleWorkContextAuthority struct {
	// Resolved live consent; sealing these facts is the separate Work Context cutover.
	InstallationID       string
	InstallationRevision uint64
	ProducerEpoch        uint64
	PrincipalID          string
	Tenant               string
	// Execution is what BindExecution established about the caller: the pod,
	// its ServiceAccount, the image it is running and the approved build's
	// incarnation. Every mint carries one; there is no unbound module identity.
	Execution ExecutionIdentity

	// The immutable registry used for the entire mint, including its release fence.
	snapshot *installedModuleRegistry
}

// ModuleAuthorizeWorkContext resolves the identity a composed module may be
// issued a Work Context for.
//
// The prefix names the principal the caller asks to act as, and is a CLAIM: the
// projected token must back it. BindExecution holds the token's ServiceAccount
// and namespace against the workload the principal declares, and the running
// image against the approved build, so a module holding its own valid token
// cannot mint as another prefix. A tenant-pinned principal keeps its declared
// tenant. A shared principal
// selects an organization whose live installation must authorize the mint.
func (s *Service) ModuleAuthorizeWorkContext(
	ctx context.Context, prefix, serviceAccountToken string, requestedOrganization ...string,
) (ModuleWorkContextAuthority, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return ModuleWorkContextAuthority{}, fenceErr
	}
	defer releaseAuthority()

	snapshot := s.moduleRegistrySnapshot()
	identity, grant, err := s.authorizeModuleIdentity(ctx, snapshot, prefix, serviceAccountToken)
	if err != nil {
		return identity, err
	}
	identity.Tenant, err = moduleMintOrganization(grant, requestedOrganization)
	if err != nil {
		return ModuleWorkContextAuthority{}, err
	}
	installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: identity.PrincipalID, BoundOrg: identity.Tenant})
	if err != nil {
		if errors.Is(err, errModuleInstalledNowhere) {
			// Proven, admitted, and installed nowhere: a precondition this
			// workload waits on, not a judgement of it. Only here — a
			// capability's use and a headless operation have no install step
			// to wait for, and hear the refusal.
			return ModuleWorkContextAuthority{}, wrapStatus(codes.FailedPrecondition, errModuleInstalledNowhere,
				"principal %s in organization %s", identity.PrincipalID, identity.Tenant)
		}
		return ModuleWorkContextAuthority{}, err
	}
	identity.InstallationID, identity.InstallationRevision, identity.ProducerEpoch = installed.InstallationID, installed.InstallationRevision, installed.ProducerEpoch
	return identity, nil
}

// authorizeModuleIdentity derives every identity fact from the mint's one
// fenced capture. Binding and the operation/source resolvers receive that same
// capture, so a replacement cannot lend an old workload a new grant or tenant.
func (s *Service) authorizeModuleIdentity(
	ctx context.Context, snapshot *installedModuleRegistry, prefix, serviceAccountToken string,
) (ModuleWorkContextAuthority, ModulePrincipalGrant, error) {
	if !registrationIdentityPattern.MatchString(prefix) || len(prefix) > 63 {
		return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, fmt.Errorf("%w: malformed prefix", ErrModuleIdentityDenied)
	}
	principalID := ModulePrincipalID(prefix)
	grant, registered, err := s.currentModuleGrantFromSnapshot(ctx, snapshot, principalID)
	if err != nil {
		return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, err
	}
	if !registered {
		// A workload that starts beside its own delivery asks before the pass
		// that reads it has run. Only here — the mint that names a prefix — is
		// that "not established yet": every other caller of the registry,
		// a person's revision check among them, hears what it always did.
		if s.deliveredSince(ctx, snapshot) {
			s.requestRegistryPass()
			return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, wrapStatus(codes.Unavailable, ErrModuleRegistryStale,
				"principal %s: a document was delivered after the registry was derived", principalID)
		}
		reason := "the module registry holds no entry for it"
		switch {
		case snapshot == nil || (len(snapshot.registry) == 0 && snapshot.envelopeRevision == 0):
			reason = "the module registry is empty and was derived from no module envelope"
		case len(snapshot.registry) == 0:
			reason = "the module registry is empty"
		}
		err := fmt.Errorf("%w: %s is not a declared module principal: %s", ErrModuleIdentityDenied, prefix, reason)
		// The caller hears only the denial (see ErrModuleIdentityDenied), so
		// the reason is said here or nowhere.
		wool.Get(ctx).In("ModuleAuthorizeWorkContext").Warn(
			"module identity refused", wool.Field("prefix", prefix), wool.ErrField(err))
		return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, err
	}
	execution, err := s.BindExecution(ctx, snapshot, principalID, serviceAccountToken)
	if err != nil {
		if errors.Is(err, ErrExecutionUnbound) {
			return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, err
		}
		wool.Get(ctx).In("ModuleAuthorizeWorkContext").Warn(
			"module identity refused", wool.Field("prefix", prefix), wool.ErrField(err))
		return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, fmt.Errorf("%w: %w", ErrModuleIdentityDenied, err)
	}
	identity := ModuleWorkContextAuthority{
		PrincipalID: principalID,
		Tenant:      grant.Tenant,
		Execution:   *execution,
		snapshot:    snapshot,
	}
	if err := s.FenceModuleMint(ctx, identity); err != nil {
		return ModuleWorkContextAuthority{}, ModulePrincipalGrant{}, err
	}
	return identity, grant, nil
}

// FenceModuleMint withholds a capability if its captured registry was replaced
// or a different envelope revision committed while it was being minted. Call
// after signing and recording, immediately before returning the token. The
// request's earlier memoized revision cannot answer this final check.
func (s *Service) FenceModuleMint(ctx context.Context, authority ModuleWorkContextAuthority) error {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()

	snapshot := authority.snapshot
	if snapshot == nil {
		return wrapStatus(codes.Unavailable, ErrModuleRegistryStale, "module mint has no registry snapshot")
	}
	fresh := context.WithValue(ctx, moduleEnvelopeRevisionMemoKey{}, &moduleEnvelopeRevisionMemo{})
	if err := s.fenceModuleRegistrySnapshot(fresh, snapshot); err != nil {
		return err
	}
	if authority.InstallationID != "" {
		live, _, err := s.authorizeModuleCapabilityFromSnapshot(fresh, snapshot, ModuleCaller{PrincipalID: authority.PrincipalID, BoundOrg: authority.Tenant})
		if err != nil {
			return err
		}
		if live.InstallationID != authority.InstallationID || live.InstallationRevision != authority.InstallationRevision {
			return wrapStatus(codes.PermissionDenied, ErrModuleAuthorityStale, "installation changed during mint")
		}
	}
	if s.modulePrincipals.Load() != snapshot {
		return wrapStatus(codes.Unavailable, ErrModuleRegistryStale, "module registry was replaced during the mint")
	}
	return nil
}

// ErrModuleTenantUnknown reports that a module principal's declared tenant
// names no organization in this database.
var ErrModuleTenantUnknown = errors.New("module principal tenant names no organization")

// VerifyModuleTenant confirms the tenant a grant declares actually exists
// before a capability is sealed to it.
//
// The registry can only check the tenant's *form*. Nothing after it checked
// either: ModuleAuthorizeWorkContext copies the value through, and
// authorizeTenant compares the sealed tenant against itself, which always
// passes. A tenant uuid that names no organization — a typo, or an id carried
// over from another environment — therefore signed cleanly and bound every call
// to a tenant nobody can see.
//
// That failure is silent all the way down: audit_events.org_id and
// domain_events.tenant_id are bare UUID columns with no foreign key to
// organizations, so the writes succeed and nothing ever raises. The mint is
// the last point that holds both the tenant and a database handle, so it is
// where the check belongs — and it closes every route at once, including
// configuration this module never generated.
func (s *Service) VerifyModuleTenant(ctx context.Context, authority ModuleWorkContextAuthority) error {
	var exists bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var checkErr error
		exists, checkErr = s.store.OrganizationIDExists(ctx, authority.Tenant)
		return checkErr
	}); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: principal %s declares tenant %s",
			ErrModuleTenantUnknown, authority.PrincipalID, authority.Tenant)
	}
	return nil
}

// RecordModuleWorkContextMint commits the durable record of an issued module
// capability. It is written after the thing it describes exists, and the caller
// withholds the capability when the record cannot be committed — so the spine
// never carries an issuance that did not happen, and never misses one that did.
//
// The record names the execution the mint was bound to, so "which pod, running
// which build, obtained this identity" is answered by the audit trail rather than
// by the token.
func (s *Service) RecordModuleWorkContextMint(
	ctx context.Context, prefix string, authority ModuleWorkContextAuthority,
) error {
	payload := map[string]any{"prefix": prefix, "tenant": authority.Tenant}
	for key, value := range authority.Execution.auditFields() {
		payload[key] = value
	}
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, authority.PrincipalID, "system", EventModuleWorkContextMint, "module", prefix,
			authority.Tenant, payload)
	})
}

// auditFields is the execution as a mint record carries it.
func (e ExecutionIdentity) auditFields() map[string]any {
	return map[string]any{
		"namespace":       e.Namespace,
		"service_account": e.ServiceAccount,
		"pod":             e.PodName,
		"pod_uid":         e.PodUID,
		"container":       e.ContainerName,
	}
}

// A shared principal selects consent, not authority: execution still proves the
// principal and the subsequent live read must find that org's installation.
// A tenant-pinned principal cannot switch orgs even if it has cross_tenant.
func moduleMintOrganization(grant ModulePrincipalGrant, requested []string) (string, error) {
	if len(requested) > 1 {
		return "", wrapStatus(codes.InvalidArgument, ErrModuleTenantNotCurrent, "one organization required")
	}
	org := ""
	if len(requested) == 1 {
		org = requested[0]
	}
	if org != "" {
		if id, err := uuid.Parse(org); err != nil || id.String() != org {
			return "", wrapStatus(codes.InvalidArgument, ErrModuleTenantNotCurrent, "organization must be a canonical UUID")
		}
	}
	if grant.Tenant != "" {
		if org != "" && org != grant.Tenant {
			return "", wrapStatus(codes.PermissionDenied, ErrModuleTenantNotCurrent, "requested organization differs from pinned tenant")
		}
		return grant.Tenant, nil
	}
	if !grant.CrossTenant || org == "" {
		return "", wrapStatus(codes.PermissionDenied, ErrModuleTenantNotCurrent, "shared mint requires cross_tenant and an organization")
	}
	return org, nil
}
