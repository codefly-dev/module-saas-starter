//go:build !pure

package infra_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
)

// Connect-time source delegations against the real schema (migration 9): the
// connect and reconnect paths that record them, the events that revoke them in
// their own transaction, the mint's re-check of every fact, and the revision
// check a consumer runs at every hop.

const (
	delegationModule       = "docstore"
	delegationModuleSecret = "docstore-secret"
	delegationBinding      = "source-sync"
	otherModule            = "reports"
	otherModuleSecret      = "reports-secret"
	runtimeModule          = "runtime"
)

func delegationDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// delegationRegistry declares three modules, every one bound to otherTenant and
// none holding cross_tenant — the delegation, never the declared tenancy, is
// what admits an organization:
//   - docstore, whose one binding accepts source delegations and is addressed
//     to the runtime module;
//   - reports, which accepts delegations too, so "another module's delegation"
//     is a real, minted one;
//   - runtime, which exchanges a delegation-bearing parent addressed to it
//     through its own "ingest" binding.
func delegationRegistry(t *testing.T, otherTenant string) business.ModulePrincipalRegistry {
	t.Helper()
	return delegationRegistryWith(t, otherTenant, false)
}

func delegationRegistryWith(t *testing.T, otherTenant string, crossTenant bool) business.ModulePrincipalRegistry {
	t.Helper()
	cross := ""
	if crossTenant {
		cross = `"cross_tenant":true,`
	}
	registry, err := business.ParseModulePrincipalRegistry(`{` +
		`"` + delegationModule + `":{"tenant":"` + otherTenant + `",` + cross + `"operation_audiences":{` +
		`"` + delegationBinding + `":{"audience":"` + runtimeModule + `",` +
		`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"collections","actions":["read","write"]}]}}},` +
		`"` + runtimeModule + `":{"tenant":"` + otherTenant + `",` + cross + `"operation_audiences":{` +
		`"ingest":{"audience":"docstore-ingest",` +
		`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}]}}},` +
		`"` + otherModule + `":{"tenant":"` + otherTenant + `","operation_audiences":{` +
		`"sync":{"audience":"reportservice",` +
		`"invoke_scopes":[{"resource_kind":"reports","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"reports","actions":["read"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"reports","actions":["write"]}]}}}}`)
	require.NoError(t, err)
	return registry
}

// delegationAudit captures every record and rejects a payload the registry
// would, as the durable emitter does in production (where an undeclared key
// drops the payload with only a log line).
type delegationAudit struct {
	mu      sync.Mutex
	entries []business.AuditEntry
}

func (a *delegationAudit) Emit(_ context.Context, entry business.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
}

func (a *delegationAudit) EmitTx(ctx context.Context, entry business.AuditEntry) error {
	if err := business.ValidatePayload(entry.EventType, entry.Payload); err != nil {
		return err
	}
	a.Emit(ctx, entry)
	return nil
}

// of lists the records of one event about one source's delegation to the
// docstore module — the module these tests follow; a connect records one for
// every accepting module.
func (a *delegationAudit) of(event business.EventType, sourceID string) []business.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []business.AuditEntry
	for _, entry := range a.entries {
		if entry.EventType == event && entry.ResourceID == sourceID && entry.Payload["module"] == delegationModule {
			out = append(out, entry)
		}
	}
	return out
}

type delegationCipher struct{}

func (delegationCipher) EncryptSecret(_ context.Context, purpose, plaintext string) (string, error) {
	return "test:" + purpose + ":" + plaintext, nil
}

func (delegationCipher) DecryptSecret(_ context.Context, purpose, envelope string) (string, error) {
	return strings.TrimPrefix(envelope, "test:"+purpose+":"), nil
}

type delegationProducer struct{}

func (delegationProducer) EnqueueJob(context.Context, *jobsv1.EnqueueJobRequest) (*jobsv1.EnqueueJobResponse, error) {
	return &jobsv1.EnqueueJobResponse{JobId: business.NewIDString()}, nil
}

// delegationGitHub accepts any token for any repository and branch.
type delegationGitHub struct{}

func (delegationGitHub) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (delegationGitHub) ResolveCommit(context.Context, string, string) (string, error) {
	return "0123456789abcdef0123456789abcdef01234567", nil
}
func (delegationGitHub) OpenRepository(context.Context, github.Workspace, string) (business.GitHubRepository, error) {
	return nil, errors.New("no repository in this test")
}
func (delegationGitHub) RepositoryIsPublic(context.Context, string) (bool, error) { return false, nil }

type delegationWorld struct {
	svc      *business.Service
	audit    *delegationAudit
	org      string
	admin    string
	other    string // a second administrator, so removing one keeps the org administered
	otherOrg string
}

func newDelegationWorld(t *testing.T) *delegationWorld {
	t.Helper()
	w := &delegationWorld{admin: seedUser(t), other: seedUser(t)}
	w.org = seedOrg(t, w.admin)
	seedOrgAdministrator(t, w.org, w.admin)
	seedOrgAdministrator(t, w.org, w.other)
	w.otherOrg = seedOrg(t, w.other)
	seedOrgAdministrator(t, w.otherOrg, w.other)
	w.svc = w.service(t, delegationRegistry(t, w.otherOrg))
	return w
}

func (w *delegationWorld) service(t *testing.T, registry business.ModulePrincipalRegistry) *business.Service {
	t.Helper()
	svc, err := business.NewService(testStore)
	require.NoError(t, err)
	// Revoking a principal or a delegation is a witnessed narrowing, so a
	// service with no policy log refuses it outright; see wireNarrowingPolicyLog.
	wireNarrowingPolicyLog(svc)
	secrets, err := business.ParseRegistrationSecrets(delegationModule + ":" + delegationDigest(delegationModuleSecret) +
		"," + otherModule + ":" + delegationDigest(otherModuleSecret))
	require.NoError(t, err)
	svc.SetModuleIdentitySecrets(secrets)
	svc.SetModuleAuthorityReads(currentModuleAuthority{}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	svc.SetEntitlementChecker(business.NewDefaultEntitlementChecker(testStore))
	svc.SetDatasourceConnector(delegationCipher{}, delegationProducer{}, "")
	svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return delegationGitHub{} })
	if w.audit == nil {
		w.audit = &delegationAudit{}
	}
	svc.SetAuditEmitter(w.audit)
	return svc
}

// connect adds a GitHub source through the real connect path, as actorID.
func (w *delegationWorld) connect(t *testing.T, orgID, actorID string) string {
	t.Helper()
	source, err := w.svc.AddSource(testCtx, actorID, business.AddSourceInput{
		OrgID: orgID, Provider: business.DatasourceProviderGitHub, CollectionLabel: "handbook-" + business.NewIDString()[:8],
		Credential: "pat-initial", Repo: "acme/handbook", Branch: "main",
	})
	require.NoError(t, err)
	return source.ID
}

func (w *delegationWorld) active(t *testing.T, orgID, sourceID string) []*business.SourceDelegation {
	t.Helper()
	list, err := w.svc.ListSourceDelegations(testCtx, orgID, sourceID, false)
	require.NoError(t, err)
	return list
}

func (w *delegationWorld) only(t *testing.T, orgID, sourceID, module string) *business.SourceDelegation {
	t.Helper()
	list, err := w.svc.ListSourceDelegations(testCtx, orgID, sourceID, true)
	require.NoError(t, err)
	var found []*business.SourceDelegation
	for _, d := range list {
		if d.ModulePrefix == module {
			found = append(found, d)
		}
	}
	require.Len(t, found, 1)
	return found[0]
}

func (w *delegationWorld) mint(ref business.SourceDelegationRef) (business.SourceOperationContextAuthority, error) {
	return w.svc.AuthorizeSourceOperationContext(testCtx, delegationModule, delegationModuleSecret, ref)
}

// subjects is what a consumer's SDK sends for a minted context: the owner with
// the authority scopes, then the module actor with its granted scopes.
func subjects(authority business.SourceOperationContextAuthority) []business.ModuleOperationRevisionSubject {
	return []business.ModuleOperationRevisionSubject{
		{PrincipalID: authority.OwnerPrincipalID, Scopes: authority.Scopes},
		{PrincipalID: authority.PrincipalID, Scopes: authority.Scopes},
	}
}

func (w *delegationWorld) confirm(authority business.SourceOperationContextAuthority) error {
	handled, err := w.svc.CheckSourceDelegationContextRevision(testCtx, authority.Tenant, authority.OwnerPrincipalID, authority.Revision, subjects(authority))
	if !handled {
		return errors.New("revision check did not recognise a source delegation context")
	}
	return err
}

func execControlPlane(t *testing.T, query string, args ...any) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := storetx.Tx(ctx)
		_, err := tx.Exec(ctx, query, args...)
		return err
	}))
}

// A connect records one delegation per accepting binding in the organization;
// a mint from it carries the source's org, the person, and exactly the
// binding's audience and delegation scopes; its use is recorded; and the
// revision check confirms the context it yields.
func TestSourceDelegation_ConnectThenMint(t *testing.T) {
	w := newDelegationWorld(t)
	sourceID := w.connect(t, w.org, w.admin)

	// Every module whose binding declares source_delegation_scopes receives one,
	// though both are bound to another organization and neither holds
	// cross_tenant; the runtime module declares no such binding and receives
	// none.
	require.Len(t, w.active(t, w.org, sourceID), 2)
	delegation := w.only(t, w.org, sourceID, delegationModule)
	require.Equal(t, delegationModule, delegation.ModulePrefix)
	require.Equal(t, delegationBinding, delegation.BindingID)
	require.Equal(t, w.admin, delegation.PrincipalID)
	require.Equal(t, w.org, delegation.OrgID)
	created := w.audit.of(business.EventSourceDelegationCreated, sourceID)
	require.Len(t, created, 1)
	require.Equal(t, w.admin, created[0].ActorID)
	require.Equal(t, delegation.ID, created[0].Payload["delegation_id"])

	for name, ref := range map[string]business.SourceDelegationRef{
		"by delegation": {DelegationID: delegation.ID},
		"by source":     {SourceID: sourceID},
	} {
		t.Run(name, func(t *testing.T) {
			authority, err := w.mint(ref)
			require.NoError(t, err)
			require.Equal(t, w.org, authority.Tenant, "the tenant is the source's organization")
			require.Equal(t, w.admin, authority.OwnerPrincipalID)
			require.Equal(t, business.ModulePrincipalID(delegationModule), authority.PrincipalID)
			require.Equal(t, runtimeModule, authority.Audience)
			require.Equal(t, []business.ModuleOperationScope{{ResourceKind: "collections", Actions: []string{"read", "write"}}}, authority.Scopes)
			require.Equal(t, delegation.ID, authority.Delegation.ID)
			require.NotZero(t, authority.Revision)

			require.NoError(t, w.svc.RecordSourceOperationContextMint(testCtx, authority))
			require.NoError(t, w.confirm(authority))
		})
	}
	used := w.audit.of(business.EventSourceDelegationUsed, sourceID)
	require.Len(t, used, 2)
	require.Equal(t, "system", used[0].ActorType)
	require.Equal(t, []string{"collections:read", "collections:write"}, used[0].Payload["scopes"])
}

func TestSourceDelegation_ExplicitRevoke(t *testing.T) {
	w := newDelegationWorld(t)
	sourceID := w.connect(t, w.org, w.admin)
	delegation := w.only(t, w.org, sourceID, delegationModule)
	authority, err := w.mint(business.SourceDelegationRef{DelegationID: delegation.ID})
	require.NoError(t, err)

	revoked, err := w.svc.RevokeSourceDelegation(testCtx, w.other, w.org, delegation.ID)
	require.NoError(t, err)
	require.Equal(t, business.SourceDelegationRevokedByAdmin, revoked.RevokedReason)
	require.Equal(t, w.other, revoked.RevokedBy)
	require.NotNil(t, revoked.RevokedAt)

	_, err = w.mint(business.SourceDelegationRef{DelegationID: delegation.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
	_, err = w.mint(business.SourceDelegationRef{SourceID: sourceID})
	require.ErrorIs(t, err, business.ErrSourceDelegationMissing, "a source with no active delegation is DELEGATION_MISSING")
	require.ErrorIs(t, w.confirm(authority), business.ErrSourceDelegationContextStale, "revocation invalidates an outstanding context")
	require.ErrorIs(t, w.svc.RecordSourceOperationContextMint(testCtx, authority), business.ErrSourceDelegationRevoked,
		"a capability signed before the revocation committed is withheld")

	events := w.audit.of(business.EventSourceDelegationRevoked, sourceID)
	require.Len(t, events, 1)
	require.Equal(t, business.SourceDelegationRevokedByAdmin, events[0].Payload["reason"])

	// Revoking again changes and records nothing.
	again, err := w.svc.RevokeSourceDelegation(testCtx, w.other, w.org, delegation.ID)
	require.NoError(t, err)
	require.Equal(t, revoked.RevokedAt.UTC(), again.RevokedAt.UTC())
	require.Len(t, w.audit.of(business.EventSourceDelegationRevoked, sourceID), 1)
}

// Leaving the organization ends the delegation: through RemoveOrgMember in the
// same transaction, and — when a writer bypasses that path — at the next mint.
func TestSourceDelegation_PersonLeavesTheOrganization(t *testing.T) {
	t.Run("removed through the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		delegation := w.only(t, w.org, sourceID, delegationModule)
		authority, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.NoError(t, err)

		require.NoError(t, w.svc.RemoveOrgMember(testCtx, w.other, &gen.RemoveOrgMemberRequest{OrgId: w.org, UserId: w.admin}))

		require.Equal(t, business.SourceDelegationMemberRemoved, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
		_, err = w.mint(business.SourceDelegationRef{DelegationID: delegation.ID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.ErrorIs(t, w.confirm(authority), business.ErrSourceDelegationContextStale)
	})
	t.Run("membership deleted behind the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		authority, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.NoError(t, err)

		execControlPlane(t, `DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`, w.org, w.admin)

		require.ErrorIs(t, w.confirm(authority), business.ErrSourceDelegationContextStale)
		_, err = w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.Equal(t, business.SourceDelegationMemberRemoved, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
		events := w.audit.of(business.EventSourceDelegationRevoked, sourceID)
		require.Len(t, events, 1)
		require.Equal(t, "system", events[0].ActorType)
	})
	t.Run("account deleted", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		execControlPlane(t, `UPDATE users SET status = 'deleted' WHERE uuid = $1`, w.admin)

		_, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.Equal(t, business.SourceDelegationUserInactive, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
	})
}

// Losing the administrator role that let the person connect ends it too.
func TestSourceDelegation_PersonLosesTheConnectPermission(t *testing.T) {
	t.Run("demoted through the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		authority, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.NoError(t, err)

		require.NoError(t, w.svc.AddOrgMember(testCtx, w.other, &gen.AddOrgMemberRequest{
			OrgId: w.org, UserId: w.admin, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		}))

		require.Equal(t, business.SourceDelegationPermissionLost, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
		_, err = w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.ErrorIs(t, err, business.ErrSourceDelegationMissing)
		require.ErrorIs(t, w.confirm(authority), business.ErrSourceDelegationContextStale)
	})
	t.Run("role changed behind the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		execControlPlane(t, `UPDATE organization_members SET role = 'member' WHERE org_id = $1 AND user_id = $2`, w.org, w.admin)

		_, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.Equal(t, business.SourceDelegationPermissionLost, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
	})
}

func TestSourceDelegation_SourceDeleted(t *testing.T) {
	t.Run("through the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		delegation := w.only(t, w.org, sourceID, delegationModule)
		authority, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.NoError(t, err)

		require.NoError(t, w.svc.DeleteDatasourceSource(testCtx, w.admin, w.org, sourceID))

		kept := w.only(t, w.org, sourceID, delegationModule)
		require.Equal(t, business.SourceDelegationSourceDeleted, kept.RevokedReason, "the record outlives its source")
		_, err = w.mint(business.SourceDelegationRef{DelegationID: delegation.ID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.ErrorIs(t, w.confirm(authority), business.ErrSourceDelegationContextStale)
		require.Len(t, w.audit.of(business.EventSourceDelegationRevoked, sourceID), 1)
	})
	t.Run("deleted behind the service", func(t *testing.T) {
		w := newDelegationWorld(t)
		sourceID := w.connect(t, w.org, w.admin)
		execControlPlane(t, `DELETE FROM datasource_sources WHERE org_id = $1 AND id = $2`, w.org, sourceID)

		_, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
		require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
		require.Equal(t, business.SourceDelegationSourceDeleted, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
	})
}

// A delegation id is never usable outside its own module and organization, and
// the refusal does not say whether it exists.
func TestSourceDelegation_CrossOrganizationAndCrossModule(t *testing.T) {
	w := newDelegationWorld(t)
	sourceA := w.connect(t, w.org, w.admin)
	delegationA := w.only(t, w.org, sourceA, delegationModule)
	reportsA := w.only(t, w.org, sourceA, otherModule)
	sourceB := w.connect(t, w.otherOrg, w.other)
	require.Len(t, w.active(t, w.otherOrg, sourceB), 2)
	delegationB := w.only(t, w.otherOrg, sourceB, otherModule)

	// Another module's delegation is refused, in both directions; by source,
	// each module resolves only its own.
	_, err := w.svc.AuthorizeSourceOperationContext(testCtx, otherModule, otherModuleSecret, business.SourceDelegationRef{DelegationID: delegationA.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationInvalid)
	_, err = w.mint(business.SourceDelegationRef{DelegationID: reportsA.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationInvalid)
	bySource, err := w.svc.AuthorizeSourceOperationContext(testCtx, otherModule, otherModuleSecret, business.SourceDelegationRef{SourceID: sourceA})
	require.NoError(t, err)
	require.Equal(t, reportsA.ID, bySource.Delegation.ID)
	// ... and indistinguishable from one that does not exist.
	_, err = w.svc.AuthorizeSourceOperationContext(testCtx, otherModule, otherModuleSecret, business.SourceDelegationRef{DelegationID: business.NewIDString()})
	require.ErrorIs(t, err, business.ErrSourceDelegationInvalid)

	// Both organizations are reached through their delegations alone.
	authorityB, err := w.svc.AuthorizeSourceOperationContext(testCtx, otherModule, otherModuleSecret, business.SourceDelegationRef{DelegationID: delegationB.ID})
	require.NoError(t, err)
	require.Equal(t, w.otherOrg, authorityB.Tenant)
	require.Equal(t, "reportservice", authorityB.Audience)

	// A cross-tenant grant adds nothing: another module's delegation stays
	// refused however widely the caller is declared.
	_, err = w.service(t, delegationRegistryWith(t, w.otherOrg, true)).AuthorizeSourceOperationContext(testCtx, otherModule, otherModuleSecret, business.SourceDelegationRef{DelegationID: delegationA.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationInvalid)

	// A context minted in one organization is not confirmed for another.
	authorityA, err := w.mint(business.SourceDelegationRef{DelegationID: delegationA.ID})
	require.NoError(t, err)
	authorityA.Tenant = w.otherOrg
	require.ErrorIs(t, w.confirm(authorityA), business.ErrSourceDelegationContextStale)

	// The administrator surface is organization-scoped the same way.
	_, err = w.svc.RevokeSourceDelegation(testCtx, w.other, w.otherOrg, delegationA.ID)
	require.ErrorIs(t, err, business.ErrSourceDelegationNotFound)
	for _, d := range w.active(t, w.otherOrg, "") {
		require.NotEqual(t, delegationA.ID, d.ID)
	}
	require.True(t, w.only(t, w.org, sourceA, delegationModule).Active(), "a refused cross-org revoke leaves the delegation alone")
}

// Reconnecting — replacing the source's credential — revokes the previous
// delegation and records a new one under the reconnecting person, atomically.
func TestSourceDelegation_ReconnectReplaces(t *testing.T) {
	w := newDelegationWorld(t)
	sourceID := w.connect(t, w.org, w.admin)
	previous := w.only(t, w.org, sourceID, delegationModule)
	outstanding, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
	require.NoError(t, err)

	_, err = w.svc.SyncDatasourceSource(testCtx, w.other, w.org, sourceID, "pat-replacement")
	require.NoError(t, err)

	all, err := w.svc.ListSourceDelegations(testCtx, w.org, sourceID, true)
	require.NoError(t, err)
	require.Len(t, all, 4, "both accepting modules' delegations are replaced")
	var list []*business.SourceDelegation
	for _, d := range all {
		if d.ModulePrefix == delegationModule {
			list = append(list, d)
		}
	}
	require.Len(t, list, 2)
	var active, replaced *business.SourceDelegation
	for _, d := range list {
		if d.Active() {
			active = d
		} else {
			replaced = d
		}
	}
	require.NotNil(t, active)
	require.NotNil(t, replaced)
	require.Equal(t, previous.ID, replaced.ID)
	require.Equal(t, business.SourceDelegationReplaced, replaced.RevokedReason)
	require.Equal(t, w.other, active.PrincipalID, "the delegation moves to the person who reconnected")

	authority, err := w.mint(business.SourceDelegationRef{SourceID: sourceID})
	require.NoError(t, err)
	require.Equal(t, w.other, authority.OwnerPrincipalID)
	require.Equal(t, active.ID, authority.Delegation.ID)
	_, err = w.mint(business.SourceDelegationRef{DelegationID: previous.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
	require.ErrorIs(t, w.confirm(outstanding), business.ErrSourceDelegationContextStale)

	require.Len(t, w.audit.of(business.EventSourceDelegationCreated, sourceID), 2)
	revoked := w.audit.of(business.EventSourceDelegationRevoked, sourceID)
	require.Len(t, revoked, 1)
	require.Equal(t, business.SourceDelegationReplaced, revoked[0].Payload["reason"])
}

// Existing sources are not migrated: a source connected before any binding
// accepted delegations has none, and says so distinctly.
func TestSourceDelegation_MissingForASourceConnectedBefore(t *testing.T) {
	w := newDelegationWorld(t)
	before := &delegationWorld{audit: w.audit}
	svcBefore := before.service(t, business.ModulePrincipalRegistry{})
	source, err := svcBefore.AddSource(testCtx, w.admin, business.AddSourceInput{
		OrgID: w.org, Provider: business.DatasourceProviderGitHub, CollectionLabel: "legacy-" + business.NewIDString()[:8],
		Credential: "pat-initial", Repo: "acme/legacy", Branch: "main",
	})
	require.NoError(t, err)
	require.Empty(t, w.active(t, w.org, source.ID))

	_, err = w.mint(business.SourceDelegationRef{SourceID: source.ID})
	require.ErrorIs(t, err, business.ErrSourceDelegationMissing)
}

// A binding whose delegation scopes change after the connect no longer confers
// what the person delegated; the mint refuses and records why.
func TestSourceDelegation_BindingChanged(t *testing.T) {
	w := newDelegationWorld(t)
	sourceID := w.connect(t, w.org, w.admin)

	widened, err := business.ParseModulePrincipalRegistry(`{"` + delegationModule + `":{"tenant":"` + w.otherOrg + `","operation_audiences":{` +
		`"` + delegationBinding + `":{"audience":"` + runtimeModule + `",` +
		`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
		`"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
		`"source_delegation_scopes":[{"resource_kind":"collections","actions":["read"]}]}}}}`)
	require.NoError(t, err)
	_, err = w.service(t, widened).AuthorizeSourceOperationContext(testCtx, delegationModule, delegationModuleSecret, business.SourceDelegationRef{SourceID: sourceID})
	require.ErrorIs(t, err, business.ErrSourceDelegationRevoked)
	require.Equal(t, business.SourceDelegationBindingChanged, w.only(t, w.org, sourceID, delegationModule).RevokedReason)
}

// publicDelegationGitHub serves every repository to a request with no credential.
type publicDelegationGitHub struct{ delegationGitHub }

func (publicDelegationGitHub) RepositoryIsPublic(context.Context, string) (bool, error) {
	return true, nil
}

// A public source holds no credential to replace, so reconnecting it without
// one moves its delegation to the person who reconnected — exactly as a
// credential reconnect does — and the previous one is revoked as replaced.
func TestSourceDelegation_PublicSourceReconnectsWithoutACredential(t *testing.T) {
	w := newDelegationWorld(t)
	w.svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return publicDelegationGitHub{} })
	source, err := w.svc.AddSource(testCtx, w.admin, business.AddSourceInput{
		OrgID: w.org, Provider: business.DatasourceProviderGitHub, CollectionLabel: "handbook-" + business.NewIDString()[:8],
		Repo: "acme/handbook", Branch: "main",
	})
	require.NoError(t, err)
	previous := w.only(t, w.org, source.ID, delegationModule)
	require.Equal(t, w.admin, previous.PrincipalID)

	_, err = w.svc.ReconnectDatasourceSource(testCtx, w.other, w.org, source.ID, "")
	require.NoError(t, err)

	var active, replaced *business.SourceDelegation
	all, err := w.svc.ListSourceDelegations(testCtx, w.org, source.ID, true)
	require.NoError(t, err)
	for _, d := range all {
		if d.ModulePrefix != delegationModule {
			continue
		}
		if d.Active() {
			active = d
		} else {
			replaced = d
		}
	}
	require.NotNil(t, active)
	require.NotNil(t, replaced)
	require.Equal(t, previous.ID, replaced.ID)
	require.Equal(t, business.SourceDelegationReplaced, replaced.RevokedReason)
	require.Equal(t, w.other, active.PrincipalID)
	authority, err := w.mint(business.SourceDelegationRef{SourceID: source.ID})
	require.NoError(t, err)
	require.Equal(t, w.other, authority.OwnerPrincipalID)
}
