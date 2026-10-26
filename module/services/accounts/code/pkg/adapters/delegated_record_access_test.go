package adapters

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"context"
	"testing"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

type exactRecordStore struct {
	business.Store
	subjects []string
	deny     string
	after    func()
}

// The audience vocabulary's one read. It declares NO solution bindings: the
// audiences these tests address are declared MODULE prefixes, installed through
// the principal registry, which is where a deployment declares them too.
func (*exactRecordStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*exactRecordStore) LiveDeclaredBindings(context.Context) ([]business.LiveDeclaredBinding, error) {
	return nil, nil
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
	r := connect.NewRequest(&gen.CheckWorkContextRecordAccessRequest{ResourceType: "rows", ResourceId: id, Action: "read", ViewerWorkContextToken: token})
	r.Header().Set(workContextHeader, callingModuleTokenFor(token))
	r.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
	return r
}
func TestExactRecordOracleRequiresSignedCurrentAttenuatedAuthority(t *testing.T) {
	_, facts, client, mint := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	declareTestAudiences(svc)
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
	narrow, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "rows", Tenant: readOrg, Owner: readOwner, Task: "task-narrow", Session: "session-narrow",
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "rows", Actions: []string{"read"}, ResourceIds: []string{"record-b"}}}})
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(narrow, "record-a"))
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

// A composed module's own identity capability is signed by the same key, for the
// same tenant, as the viewer contexts this oracle answers for — so what keeps the
// two apart is that the module mint seals no authority scopes, not the audience,
// which may legally be either the module-capability one or the module's own
// prefix. Each form is refused by a different gate, so each is asserted on the
// refusal it must come from: a denial arriving from the other gate would mean the
// one under test had stopped holding.
func TestExactRecordOracleRefusesModuleIdentityCapability(t *testing.T) {
	_, _, client, _ := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	declareTestAudiences(svc)
	module := business.ModulePrincipalID("rows")
	identity := mintModuleIdentity(t, module, readOrg)
	// The same identity re-minted at the module's own prefix, so the declared
	// vocabulary gate passes and only the empty scope set can refuse it.
	prefixed, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "rows", Tenant: readOrg, Owner: module,
		OwnerKind: business.PrincipalKindService, Task: "019f6bf7-3333-7333-8333-333333333333", Session: "019f6bf7-4444-7444-8444-444444444444"})
	for _, capability := range []struct{ name, token, refusal string }{
		// The module presenting its own identity as the viewer: that capability
		// is addressed to the module-capability audience, not to the module.
		{"module-capability audience", identity, errViewerNotAddressedToCaller},
		{"module's own prefix", prefixed, "record scope required"},
	} {
		request := connect.NewRequest(&gen.CheckWorkContextRecordAccessRequest{ResourceType: "rows", ResourceId: "record-a", Action: "read", ViewerWorkContextToken: capability.token})
		request.Header().Set(workContextHeader, identity)
		request.Header().Set("x-codefly-internal-token", "source-read-test-perimeter")
		out, err := client.CheckWorkContextRecordAccess(context.Background(), request)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), capability.name)
		require.ErrorContains(t, err, capability.refusal, capability.name)
		require.Nil(t, out, capability.name)
	}
	require.Empty(t, store.subjects)
}

// An installation capability is the headless mint: an agent principal actor under
// the installation's owner of record, carrying REAL authority scopes rather than
// the empty set a module identity seals. It therefore clears every gate that stops
// a module identity, and the only thing standing between it and the record is the
// intersection — so the agent hop must be checked against live grants exactly like
// a human delegate's, never inherited from the owner it acts for.
func TestExactRecordOracleIntersectsInstallationCapability(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	declareTestAudiences(svc)
	// The sealed revision must equal what the oracle re-resolves, or the mint would
	// be refused as stale before the intersection this test is about is reached.
	facts.installationFcts = &business.InstallationAuthorityFacts{
		OwnerPrincipalID:       readOwner,
		OrganizationRevision:   facts.facts.OrganizationRevision,
		OwnerPrincipalRevision: facts.facts.PrincipalRevision,
		Actor: &business.Principal{ID: installAgentID, Kind: business.PrincipalKindAgent,
			AgentIdentifier: "example.test/solution:1.0.0", AllowedAudiences: []string{"rows"}, AllowedScopes: []string{"rows"}},
	}
	issued, err := workContextSingleton.StartInstallationTask(
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "source-read-test-perimeter")),
		&gen.StartInstallationTaskRequest{OrgId: readOrg, InstallationId: installID, TaskId: installTaskID,
			Audience:        "rows",
			AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "rows", Actions: []string{"read"}, ResourceIds: []string{"record-a"}}}})
	require.NoError(t, err)
	require.Equal(t, installAgentID, issued.GetCurrentActorPrincipalId())
	workContextSingleton.authority = exactChainAuthority{facts: facts.facts}
	workContextSingleton.journal = &exactChainJournal{}

	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(issued.GetToken(), "record-a"))
	require.NoError(t, err)
	require.True(t, out.Msg.Allowed)
	require.Equal(t, []string{readOwner, installAgentID}, store.subjects, "the agent hop is intersected, not inherited from its owner")

	// The owner still holds the grant; revoking it from the agent alone must deny.
	store.subjects, store.deny = nil, installAgentID
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(issued.GetToken(), "record-a"))
	require.NoError(t, err)
	require.False(t, out.Msg.Allowed)
	require.Empty(t, out.Msg.ScopeNodeId)
}

// An exchanged read audience is the fourth capability shape reaching this oracle:
// authority re-audienced from a verified parent viewer context rather than minted
// fresh. Where the attenuation lands is not where it looks: once a delegation
// chain is present the sealed scope enforced here is the OUTERMOST hop's granted
// scopes, which REPLACE rather than intersect the top-level authority scopes, so
// the exchange narrows that hop and leaves the top level at the parent's width.
// The assertions below turn on that, and it is pinned rather than assumed — read
// only the top-level scopes and this capability looks kind-wide. What must hold
// is that the oracle enforces the narrowed hop, and that it still checks every hop
// against live grants rather than collapsing the chain into the owner.
func TestExactRecordOracleIntersectsExchangedReadAudienceCapability(t *testing.T) {
	_, facts, client, _ := sourceReadFixture(t)
	store := &exactRecordStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("example"): {Prefix: "example", Tenant: readOrg, Resources: []string{"rows"},
			ReadAudiences: map[string]business.ModuleReadAudience{
				"proof": {Audience: "rows", Scopes: []business.ModuleReadScope{{ResourceKind: "rows", ResourceIDs: []string{"record-a"}}}}}},
		business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}},
	})
	service = svc
	declareTestAudiences(svc)
	journal := &exactChainJournal{}
	workContextSingleton.authority = exactChainAuthority{facts: facts.facts}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "rows", Actions: []string{"read"}}}
	parent, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "example", Tenant: readOrg, Owner: readOwner,
		Task: "exchange-task", Session: "exchange-session", Scopes: scopes,
		Actors: []delegationHop{agentHop("actor-1", "hop-1", scopes...), agentHop("actor-2", "hop-2", scopes...)}})
	exchanged, err := client.ExchangeDelegatedReadAudience(context.Background(), readExchangeRequest(t, parent))
	require.NoError(t, err)
	verified, err := workContextSingleton.verifyFor(context.Background(), exchanged.Msg.Token, "rows")
	require.NoError(t, err)
	child := verified.Context()
	require.Empty(t, child.AuthorityScopes[0].ResourceIds, "the top-level scope stays at the parent's width")
	require.Equal(t, []string{"record-a"}, child.ActorChain[len(child.ActorChain)-1].GrantedScopes[0].ResourceIds,
		"the binding narrows the outermost hop, which is the scope the oracle actually enforces")

	journal.ids = nil
	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(exchanged.Msg.Token, "record-a"))
	require.NoError(t, err)
	require.True(t, out.Msg.Allowed)
	require.Equal(t, []string{readOwner, "actor-1", "actor-2"}, store.subjects, "the parent's hops are intersected, not inherited from the owner the exchange acted for")
	require.Subset(t, journal.ids, []string{"hop-1", "hop-2"}, "every hop's own journal row is asked about")

	// The binding seals record-a while the parent is kind-wide, so the exchanged
	// context must be refused for record-b before any grant is read. Presenting the
	// parent for the same record reaches the grant check instead, which is what
	// makes the refusal above attenuation rather than the record being unreachable.
	before := len(store.subjects)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(exchanged.Msg.Token, "record-b"))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.ErrorContains(t, err, "record scope required")
	require.Nil(t, out)
	require.Len(t, store.subjects, before)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(parent, "record-b"))
	require.NoError(t, err)
	require.False(t, out.Msg.Allowed)
	require.Greater(t, len(store.subjects), before)

	// Every hop is checked against live grants: revoking any one of them denies the
	// record while the rest still hold it.
	for _, hop := range []string{readOwner, "actor-1", "actor-2"} {
		store.subjects, store.deny = nil, hop
		out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(exchanged.Msg.Token, "record-a"))
		require.NoError(t, err, hop)
		require.False(t, out.Msg.Allowed, hop)
		require.Empty(t, out.Msg.ScopeNodeId, hop)
	}
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
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{business.ModulePrincipalID("rows"): {Prefix: "rows", Resources: []string{"rows"}}})
	service = svc
	declareTestAudiences(svc)
	workContextSingleton.authority = exactChainAuthority{facts: facts.facts}
	journal := &exactChainJournal{}
	workContextSingleton.journal = journal
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "rows", Actions: []string{"read"}, ResourceIds: []string{"record-a"}}}
	token, _ := mintTestCapability(t, workContextSingleton, testCapability{Audience: "rows", Tenant: readOrg, Owner: readOwner, Task: "chain-task", Session: "chain-session", Scopes: scopes,
		Actors: []delegationHop{agentHop("actor-1", "hop-1", scopes...), agentHop("actor-2", "hop-2", scopes...)}})
	out, err := client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.NoError(t, err)
	require.True(t, out.Msg.Allowed)
	require.Equal(t, []string{readOwner, "actor-1", "actor-2"}, store.subjects)
	require.Subset(t, journal.ids, []string{"hop-1", "hop-2"}, "every hop's own journal row is asked about")
	store.deny = "actor-1"
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.NoError(t, err)
	require.False(t, out.Msg.Allowed)
	store.deny = ""
	journal.revoked = true
	before := len(store.subjects)
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
	require.Len(t, store.subjects, before)
	journal.revoked = false
	store.after = func() { journal.revoked = true }
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
	workContextSingleton.journal = nil
	out, err = client.CheckWorkContextRecordAccess(context.Background(), exactRequest(token, "record-a"))
	require.Error(t, err)
	require.Nil(t, out)
}

func (s *exactRecordStore) WithSourceReadSnapshot(ctx context.Context, org string, run func(context.Context) error) error {
	return s.WithOrgTx(ctx, org, run)
}

func (s *exactRecordStore) RecordScopeNodeID(context.Context, string, string) (string, error) {
	return "placed-node", nil
}
