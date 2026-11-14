package adapters

import (
	"context"
	"crypto/ed25519"
	"sync/atomic"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Every authority question a host handler asks the core is answered from one
// snapshot of the store (oneRead): read source by source, one question took
// the control-plane pool a dozen times or more, and a burst of them queued to
// the caller's deadline. The store here counts the core's reads made outside
// a question's snapshot, so a handler that asks the core without its wrapper
// fails this test, whatever the core answers. Every Issue shape is driven past
// its own authentication (the session, the module edge, the workload) to the
// core's question; the question is the test, so a shape the core then refuses
// (an installation the solution does not act for, a delegation never granted)
// counts as long as it was asked inside its snapshot.

type snapshotProbeStore struct {
	business.Store
	core    *memstore.Store
	replay  workcontext.ReplayStore
	asked   atomic.Int64 // questions opened (WithAuthoritySnapshot)
	outside atomic.Int64 // core reads made outside any question's snapshot
}

type inProbeSnapshot struct{}

func (s *snapshotProbeStore) AuthorityCoreWithConsent(business.ModuleConsent) authority.Store {
	return probeCoreStore{s}
}

func (s *snapshotProbeStore) AuthorityCoreReplay() workcontext.ReplayStore { return s.replay }

func (s *snapshotProbeStore) WithAuthoritySnapshot(ctx context.Context, fn func(context.Context) error) error {
	s.asked.Add(1)
	return fn(context.WithValue(ctx, inProbeSnapshot{}, true))
}

// A signed-in person's membership, as the Issue handler checks it.
func (s *snapshotProbeStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	return &gen.OrgMembership{OrgId: orgID, UserId: userID, Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
}

// The issue record, which the handler makes outside the question.
func (s *snapshotProbeStore) WithOrgTx(context.Context, string, func(context.Context) error) error {
	return nil
}

type probeCoreStore struct{ s *snapshotProbeStore }

func (c probeCoreStore) Read(ctx context.Context, fn func(authority.Tx) error) error {
	if ctx.Value(inProbeSnapshot{}) == nil {
		c.s.outside.Add(1)
	}
	return c.s.core.Read(ctx, fn)
}

func (c probeCoreStore) Write(ctx context.Context, fn func(authority.Tx) error) error {
	return c.s.core.Write(ctx, fn)
}

type probeKeys struct{ public ed25519.PublicKey }

func (k probeKeys) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) {
	return map[string]ed25519.PublicKey{"k1": k.public}, nil
}

func TestEveryHostAuthorityQuestionIsOneSnapshot(t *testing.T) {
	ctx := context.Background()
	org, admin, member := uuid.NewString(), uuid.NewString(), uuid.NewString()
	const solution, records, kind = "probe-solution", "probe-records", "probe.item"
	probe := &snapshotProbeStore{core: memstore.New(), replay: workcontext.NewMemoryReplayStore()}
	probe.core.PutOrganization(domain.Organization{ID: org, Name: "Probe"})
	probe.core.PutOrgRole(domain.OrgRole{Organization: org, Name: "admin", Scopes: []domain.RoleScope{{Kind: corescope.AnyKind, Actions: []string{corescope.AnyAction}}}})
	probe.core.PutPerson(domain.Person{ID: admin, Organization: org, Roles: []string{"admin"}})
	probe.core.PutPerson(domain.Person{ID: member, Organization: org})

	// The host's core, as the handlers reach it; its boot reconciliation waits
	// for a pass this test never runs.
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	svc, err := business.NewService(probe)
	require.NoError(t, err)
	svc.SetModuleRegistryPassTrigger(func() {})
	require.NoError(t, svc.ConfigureAuthorityCore(ctx, "https://probe.test", "k1", private))
	// The workloads that authenticate: the solution's own, declared in the
	// module registry (stated directly, so no envelope fences it), and the
	// module edge a page's call comes through.
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	svc.SetModulePrincipals(business.ModulePrincipalRegistry{business.ModulePrincipalID(solution): {
		Prefix: solution, Tenant: org, Workloads: []business.ModuleWorkload{{Namespace: solution, ServiceAccount: solution, Container: "app"}}}})
	workload := &authorityv1.WorkloadCredential{Credential: cluster.Run(executiontest.Workload{Namespace: solution, ServiceAccount: solution, Container: "app"})}
	edge := &authorityv1.WorkloadCredential{Credential: cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.ModuleEdgeService, Container: "app"})}
	previous := service
	service = svc
	t.Cleanup(func() { service = previous })

	// What the questions are about, set up beside the handlers: a person
	// holding a role on a solution whose binding reaches a records module.
	setup, err := authority.New(probe.core, authority.Config{Issuer: "https://probe.test", KeyID: "k1", Signer: private,
		Keys: probeKeys{public}, Replay: probe.replay, HostInstallsModules: true})
	require.NoError(t, err)
	items := []*basev0.WorkScopeV1{{ResourceKind: kind, Actions: []string{"read"}}}
	held := []*basev0.WorkScopeV1{{ResourceKind: solution + ".note", Actions: []string{"read"}}, items[0]}
	for _, m := range []domain.Module{
		{Name: records, Principal: records, Kind: domain.KindModule, Generation: 1, OwnerAware: true, ResourceKinds: items},
		{Name: solution, Principal: solution, Kind: domain.KindSolution, Generation: 1, OwnerAware: true, ResourceKinds: held[:1],
			Bindings: map[string]domain.Binding{"records": {ID: "records", Audience: records, InvokeScopes: items}},
			Roles:    map[string][]*basev0.WorkScopeV1{domain.InstallerRole: held, "viewer": held}},
	} {
		_, err := setup.ApplyModule(ctx, m)
		require.NoError(t, err)
	}
	installer := authority.Person{ID: admin, Organization: org, SessionID: "session-admin"}
	approval, err := setup.Review(ctx, installer, org, solution)
	require.NoError(t, err)
	approval.OperationID = uuid.NewString()
	installation, err := setup.Install(ctx, installer, org, solution, domain.InstallerRole, admin, &approval, nil)
	require.NoError(t, err)
	_, err = setup.GrantRole(ctx, installer, authority.RoleRequest{Organization: org, InstallationID: installation.ID, Role: "viewer", PersonID: member})
	require.NoError(t, err)
	session := uuid.New()
	root, err := setup.IssueForPerson(ctx, authority.Person{ID: member, Organization: org, SessionID: session.String()}, authority.IssueRequest{Audience: solution})
	require.NoError(t, err)
	child, err := setup.IssueChild(ctx, root.Token, authority.Workload{Module: solution}, authority.IssueRequest{Binding: "records"})
	require.NoError(t, err)

	signedIn := accountsauth.WithVerifiedRequestIdentity(ctx, accountsauth.RequestIdentity{
		RealActor: uuid.MustParse(member), EffectiveSubject: uuid.MustParse(member), OrgID: uuid.MustParse(org), SessionID: session})
	signedIn = withCredentialKind(accountsauth.WithVerifiedDatabaseIdentity(signedIn, member, org), credentialKindSession)

	server := &HostAuthorityServer{}
	issue := func(ctx context.Context, req *authorityv1.IssueRequest) func() error {
		return func() error { _, err := server.Issue(ctx, req); return err }
	}
	for _, handler := range []struct {
		name     string
		call     func() error
		refusals bool // the core may refuse it: the question is what is counted
	}{
		{name: "Check", call: func() error {
			_, err := server.Check(ctx, &authorityv1.CheckRequest{Token: child.Token, Audience: records})
			return err
		}},
		{name: "ListResources", call: func() error {
			_, err := server.ListResources(ctx, &authorityv1.ListResourcesRequest{Token: child.Token, Audience: records, ResourceKind: kind, Action: "read"})
			return err
		}},
		{name: "Issue for a person", call: issue(signedIn, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: solution})},
		{name: "Issue from a parent token", call: issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_ParentToken{ParentToken: root.Token}, Actor: workload, OperationBindingId: "records"})},
		{name: "Issue for a page", refusals: true, call: issue(signedIn, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, PageTarget: solution, Actor: edge, Audience: records})},
		{name: "Issue for a workload", refusals: true, call: issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Workload{Workload: workload}, OperationBindingId: "records"})},
		{name: "Issue for an installation", refusals: true, call: issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Installation{Installation: &authorityv1.InstallationRef{InstallationId: installation.ID}}, Actor: workload, Audience: solution})},
		{name: "Issue through a delegation", refusals: true, call: issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Delegation{Delegation: &authorityv1.DelegationRef{DelegationId: uuid.NewString()}}, Actor: workload})},
	} {
		asked, outside := probe.asked.Load(), probe.outside.Load()
		if err := handler.call(); !handler.refusals {
			require.NoError(t, err, handler.name)
		} else if err != nil {
			t.Logf("%s: the core answered %v", handler.name, err)
		}
		require.Equal(t, int64(1), probe.asked.Load()-asked, "%s asks the core one question", handler.name)
		require.Zero(t, probe.outside.Load()-outside, "%s: every read the core makes is the question's snapshot", handler.name)
	}
}
