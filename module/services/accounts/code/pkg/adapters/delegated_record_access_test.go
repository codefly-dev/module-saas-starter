package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

type exactRecordStore struct {
	business.Store
	subjects []string
	deny     string
	after    func()
}

func (s *exactRecordStore) WithOrgTx(ctx context.Context, org string, run func(context.Context) error) error {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, readOwner); err != nil {
		return err
	}
	return run(ctx)
}
func (s *exactRecordStore) CheckAccess(ctx context.Context, subject string, kind gen.SubjectKind, resource, id, action string) (bool, string, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, readOrg, readOwner); err != nil {
		return false, "", err
	}
	s.subjects = append(s.subjects, subject)
	if s.after != nil {
		s.after()
	}
	return subject != s.deny && kind == gen.SubjectKind_SUBJECT_KIND_PRINCIPAL && resource == "rows" && id == "record-a" && action == "read", "", nil
}
func exactRequest(token, id string) *connect.Request[gen.CheckWorkContextRecordAccessRequest] {
	r := connect.NewRequest(&gen.CheckWorkContextRecordAccessRequest{ResourceType: "rows", ResourceId: id, Action: "read"})
	r.Header().Set(codefly.WorkContextHeaderName, token)
	r.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return r
}
func TestExactRecordOracleRequiresSignedCurrentAttenuatedAuthority(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	token := mint("rows", "rows", "read")
	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.NoError(t, err)
	require.True(t, out.Msg.Allowed)
	require.Equal(t, []string{readOwner}, store.subjects)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "other-tenant-record"))
	require.NoError(t, err)
	require.False(t, out.Msg.Allowed)
	for _, bad := range []string{"", token + "tampered", mint("unknown", "rows", "read"), mint("rows", "rows", "write")} {
		before := len(store.subjects)
		out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(bad, "record-a"))
		require.Error(t, err)
		require.Nil(t, out)
		require.Len(t, store.subjects, before)
	}
	narrow, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: "rows", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: "task-narrow", SessionID: "session-narrow", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: []*basev0.WorkScopeV1{{ResourceKind: "rows", Actions: []string{"read"}, ResourceIds: []string{"record-b"}}}})
	require.NoError(t, err)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(narrow.Encoded(), "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
	store.after = func() { facts.facts.OrganizationRevision++ }
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
}
func TestExactRecordOracleIntersectsEveryAuthenticatedSubject(t *testing.T) {
	store := &exactRecordStore{deny: "actor-2"}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	ctx := auth.WithVerifiedDatabaseIdentity(context.Background(), readOwner, readOrg)
	current := func(context.Context) error { return nil }
	allowed, err := svc.CheckDelegatedRecordAccess(ctx, readOrg, []string{readOwner, "actor-1", "actor-2"}, "rows", "record-a", "read", current)
	require.NoError(t, err)
	require.False(t, allowed.GetAllowed())
	require.Equal(t, []string{readOwner, "actor-1", "actor-2"}, store.subjects)
	store.deny = ""
	allowed, err = svc.CheckDelegatedRecordAccess(ctx, readOrg, []string{readOwner, "actor-1", "actor-2"}, "rows", "record-a", "read", current)
	require.NoError(t, err)
	require.True(t, allowed.GetAllowed())
}

// A composed module's installed identity capability is signed by the same key,
// for the same tenant, as the viewer contexts this oracle answers for — so what
// keeps the two apart is that the module mint seals no authority scopes, not the
// audience, which may legally be either the module-capability one or the
// module's own prefix. Neither form may stand in for a viewer's delegation.
func TestExactRecordOracleRefusesInstallationCapability(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	module := business.ModulePrincipalID("rows")
	identity, _, err := workContextSingleton.StartModuleTask(business.ModuleWorkContextAuthority{PrincipalID: module, Tenant: readOrg})
	require.NoError(t, err)
	// The same installed identity re-minted at the module's own prefix, so the
	// declared-vocabulary check passes and only the empty scope set can refuse it.
	prefixed, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: "rows", TenantID: readOrg, OwnerPrincipalID: module,
		TaskID: "install-task", SessionID: "install-session", AuthorizationRevision: facts.facts.EffectiveRevision(),
		ActorChain: []*basev0.WorkActorV1{{PrincipalId: module, PrincipalKind: "service", DelegationId: "install-hop"}}})
	require.NoError(t, err)
	for _, capability := range []string{identity.Encoded(), prefixed.Encoded()} {
		out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(capability, "record-a"))
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.Nil(t, out)
	}
	require.Empty(t, store.subjects)
}

type exactChainAuthority struct {
	business.WorkContextAuthorityStore
	facts *business.WorkContextAuthorityFacts
}

func (a exactChainAuthority) ResolveWorkContextAuthority(ctx context.Context, org, owner, actor string, _ []business.WorkContextPermission) (*business.WorkContextAuthorityFacts, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, owner); err != nil {
		return nil, err
	}
	result := *a.facts
	if actor != "" {
		result.Actor = &business.Principal{ID: actor}
	}
	return &result, nil
}

type exactChainJournal struct {
	business.ActorChainJournal
	revoked bool
	ids     []string
}

func (j *exactChainJournal) AnyActorChainHopRevoked(_ context.Context, _ string, ids []string) (bool, error) {
	j.ids = append([]string(nil), ids...)
	return j.revoked, nil
}
func TestExactRecordOracleChecksSignedActorChainRevocation(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	workContextSingleton.authority = exactChainAuthority{facts: facts.facts}
	journal := &exactChainJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "rows", Actions: []string{"read"}, ResourceIds: []string{"record-a"}}}
	token, _, err := workContextSingleton.signer.StartTask(codefly.StartTaskInput{Audience: "rows", TenantID: readOrg, OwnerPrincipalID: readOwner, TaskID: "chain-task", SessionID: "chain-session", AuthorizationRevision: facts.facts.EffectiveRevision(), AuthorityScopes: scopes, ActorChain: []*basev0.WorkActorV1{{PrincipalId: "actor-1", PrincipalKind: "service", DelegationId: "hop-1", GrantedScopes: scopes}, {PrincipalId: "actor-2", PrincipalKind: "service", DelegationId: "hop-2", GrantedScopes: scopes}}})
	require.NoError(t, err)
	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token.Encoded(), "record-a"))
	require.NoError(t, err)
	require.True(t, out.Msg.Allowed)
	require.Equal(t, []string{readOwner, "actor-1", "actor-2"}, store.subjects)
	require.Equal(t, []string{"hop-1", "hop-2"}, journal.ids)
	store.deny = "actor-1"
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token.Encoded(), "record-a"))
	require.NoError(t, err)
	require.False(t, out.Msg.Allowed)
	store.deny = ""
	journal.revoked = true
	before := len(store.subjects)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token.Encoded(), "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
	require.Len(t, store.subjects, before)
	journal.revoked = false
	store.after = func() { journal.revoked = true }
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token.Encoded(), "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
	workContextSingleton.journal = nil
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token.Encoded(), "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
}

func (s *exactRecordStore) WithSourceReadSnapshot(ctx context.Context, org string, run func(context.Context) error) error {
	return s.WithOrgTx(ctx, org, run)
}

func (s *exactRecordStore) RecordScopeNodeID(context.Context, string, string) (string, error) {
	return "placed-node", nil
}
