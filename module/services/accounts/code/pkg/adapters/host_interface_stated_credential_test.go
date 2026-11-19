package adapters

import (
	"context"
	"crypto/ed25519"
	"testing"

	authorityv1 "accounts/pkg/gen/host/authority/v1"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// statedCredentialFixture is a host whose core composes two solutions, each
// installed at one organization, and a module beside them. member holds the
// user role on audit, which reads and writes its log, and no role on other;
// outsider is a member of the organization holding no role anywhere.
type statedCredentialFixture struct {
	sessions                          *snapshotProbeStore
	server                            *HostAuthorityServer
	org, member, outsider             string
	audit, other, records             string
	auditInstallation, otherInstalled string
	publicOrigin                      string
	read, write                       *basev0.WorkScopeV1
}

func newStatedCredentialFixture(t *testing.T) statedCredentialFixture {
	t.Helper()
	ctx := context.Background()
	f := statedCredentialFixture{server: &HostAuthorityServer{}, org: uuid.NewString(), member: uuid.NewString(), outsider: uuid.NewString(),
		audit: "stated-audit", other: "stated-other", records: "stated-records", publicOrigin: "https://app.example.test"}
	admin := uuid.NewString()
	probe := &snapshotProbeStore{core: memstore.New(), replay: workcontext.NewMemoryReplayStore()}
	f.sessions = probe
	probe.core.PutOrganization(domain.Organization{ID: f.org, Name: "Acme"})
	probe.core.PutPerson(domain.Person{ID: admin, Organization: f.org, Roles: []string{"admin"}})
	probe.core.PutPerson(domain.Person{ID: f.member, Organization: f.org})
	probe.core.PutPerson(domain.Person{ID: f.outsider, Organization: f.org})

	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	svc, err := business.NewService(probe)
	require.NoError(t, err)
	svc.SetModuleRegistryPassTrigger(func() {})
	require.NoError(t, svc.ConfigureAuthorityCore(ctx, "https://stated.test", "k1", private))
	previous := service
	service = svc
	t.Cleanup(func() { service = previous })
	require.NoError(t, accountsauth.SetConfiguredPublicOrigin(f.publicOrigin))
	t.Cleanup(func() { _ = accountsauth.SetConfiguredPublicOrigin("") })

	setup, err := authority.New(probe.core, authority.Config{Issuer: "https://stated.test", KeyID: "k1", Signer: private,
		Keys: probeKeys{public}, Replay: probe.replay, HostInstallsModules: true})
	require.NoError(t, err)
	log := func(solution string, actions ...string) *basev0.WorkScopeV1 {
		return &basev0.WorkScopeV1{ResourceKind: solution + ".log", Actions: actions}
	}
	entries := []*basev0.WorkScopeV1{{ResourceKind: "stated.entry", Actions: []string{"read"}}}
	_, err = setup.ApplyModule(ctx, domain.Module{Name: f.records, Principal: f.records, Kind: domain.KindModule, Generation: 1, OwnerAware: true, ResourceKinds: entries})
	require.NoError(t, err)
	installer := authority.Person{ID: admin, Organization: f.org, SessionID: "session-admin"}
	installed := map[string]string{}
	for _, solution := range []string{f.audit, f.other} {
		kinds := []*basev0.WorkScopeV1{log(solution, "read", "write")}
		_, err := setup.ApplyModule(ctx, domain.Module{Name: solution, Principal: solution + "-principal", Kind: domain.KindSolution, Generation: 1, OwnerAware: true,
			ResourceKinds: kinds, Roles: map[string][]*basev0.WorkScopeV1{domain.AdminRole: kinds, domain.UserRole: kinds}})
		require.NoError(t, err)
		approval, err := setup.Review(ctx, installer, f.org, solution)
		require.NoError(t, err)
		approval.OperationID = uuid.NewString()
		installation, err := setup.Install(ctx, installer, f.org, solution, domain.AdminRole, admin, &approval, nil)
		require.NoError(t, err)
		installed[solution] = installation.ID
	}
	f.auditInstallation, f.otherInstalled = installed[f.audit], installed[f.other]
	_, err = setup.GrantRole(ctx, installer, authority.RoleRequest{Organization: f.org, InstallationID: f.auditInstallation, Role: domain.UserRole, PersonID: f.member})
	require.NoError(t, err)
	f.read, f.write = log(f.audit, "read"), log(f.audit, "write")
	return f
}

// principal is the principal a solution's capabilities are addressed to.
func (f statedCredentialFixture) principal(solution string) string { return solution + "-principal" }

// stated is the identity a gateway states for a credential it validated.
func (f statedCredentialFixture) stated(person, kind string, scopes ...string) context.Context {
	ctx := accountsauth.WithVerifiedRequestIdentity(context.Background(), accountsauth.RequestIdentity{
		RealActor: uuid.MustParse(person), EffectiveSubject: uuid.MustParse(person), OrgID: uuid.MustParse(f.org)})
	return withScopes(withCredentialKind(accountsauth.WithVerifiedDatabaseIdentity(ctx, person, f.org), kind), scopes)
}

// apiKey is a key owned by person carrying scopes, as the gateway states it.
func (f statedCredentialFixture) apiKey(person string, scopes ...string) context.Context {
	return f.stated(person, credentialKindAPIKey, scopes...)
}

// resourceToken is a session of person's confined to solution's MCP
// endpoint, admitted under installation, as the gateway states it.
func (f statedCredentialFixture) resourceToken(person, solution, installation string) context.Context {
	session := uuid.New()
	ctx := accountsauth.WithVerifiedRequestIdentity(context.Background(), accountsauth.RequestIdentity{
		RealActor: uuid.MustParse(person), EffectiveSubject: uuid.MustParse(person), OrgID: uuid.MustParse(f.org), SessionID: session})
	ctx = withCredentialKind(accountsauth.WithVerifiedDatabaseIdentity(ctx, person, f.org), credentialKindSession)
	ctx = accountsauth.WithVerifiedResourceConfinement(ctx, accountsauth.SolutionMCPResource(f.publicOrigin, solution))
	return accountsauth.WithVerifiedInstallation(ctx, installation)
}

func (f statedCredentialFixture) issue(ctx context.Context, audience string) (*authorityv1.IssueResponse, error) {
	return f.server.Issue(ctx, &authorityv1.IssueRequest{As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience})
}

// held is what an issued capability carries, as "kind:action" pairs.
func held(t *testing.T, issued *authorityv1.IssueResponse) []string {
	t.Helper()
	var out []string
	for _, sc := range issued.GetContext().GetAuthorityScopes() {
		for _, action := range sc.GetActions() {
			out = append(out, sc.GetResourceKind()+":"+action)
		}
	}
	return out
}

// An API key at a solution is no longer refused outright. It is issued a
// capability addressed to that solution alone, from the key's own grants: its
// scopes, within what its owner holds there through their role on the
// installation, exactly as for a person.
func TestAnAPIKeyIsIssuedItsScopesWithinWhatItsOwnerHoldsAtASolution(t *testing.T) {
	f := newStatedCredentialFixture(t)
	audience := f.principal(f.audit)

	issued, err := f.issue(f.apiKey(f.member, f.audit+".log:read"), audience)
	require.NoError(t, err)
	require.Equal(t, []string{f.audit + ".log:read"}, held(t, issued), "the key's scope, and not the write its owner also holds")
	require.Equal(t, audience, issued.GetContext().GetAudience())
	require.Equal(t, f.member, issued.GetContext().GetOwnerPrincipalId())
	require.Equal(t, f.org, issued.GetContext().GetOrganizationId())
	require.Equal(t, f.auditInstallation, issued.GetContext().GetSeal().GetInstallationId())

	// A wildcard carries what its owner holds, and no more.
	for _, wildcard := range []string{"*:*", f.audit + ".log:*", "*:read"} {
		issued, err = f.issue(f.apiKey(f.member, wildcard), audience)
		require.NoError(t, err, wildcard)
		want := []string{f.audit + ".log:read", f.audit + ".log:write"}
		if wildcard == "*:read" {
			want = want[:1]
		}
		require.ElementsMatch(t, want, held(t, issued), wildcard)
	}

	for _, refused := range []struct {
		name   string
		ctx    context.Context
		at     string
		reason string
	}{
		{"scopes that hold nothing there", f.apiKey(f.member, "users:read", f.other+".log:read"), audience, "hold nothing at"},
		{"a key with no scopes", f.apiKey(f.member), audience, "hold nothing at"},
		{"an owner who holds no role there", f.apiKey(f.outsider, "*:*"), audience, "holds no role on " + f.audit},
		{"an owner who holds no role at another solution", f.apiKey(f.member, "*:*"), f.principal(f.other), "holds no role on " + f.other},
		{"a module, which a key is issued nothing at", f.apiKey(f.member, "*:*"), f.records, "only at a solution"},
	} {
		t.Run(refused.name, func(t *testing.T) {
			_, err := f.issue(refused.ctx, refused.at)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), refused.reason)
		})
	}
	// A key makes no page's call: that is a person present at the edge.
	_, err = f.server.Issue(f.apiKey(f.member, "*:*"), &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience, PageTarget: f.audit})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// Scopes asked within the key's are issued as asked; beyond them, refused.
	asked, err := f.server.Issue(f.apiKey(f.member, "*:*"), &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience, Scopes: []*authorityv1.WorkScopeV1{wireScope(f.audit+".log", "read")}})
	require.NoError(t, err)
	require.Equal(t, []string{f.audit + ".log:read"}, held(t, asked))
	_, err = f.server.Issue(f.apiKey(f.member, f.audit+".log:read"), &authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_Person{Person: &authorityv1.PersonSession{}}, Audience: audience, Scopes: []*authorityv1.WorkScopeV1{wireScope(f.audit+".log", "write")}})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// Admission lets a key carrying any scope reach Issue, where the handler
	// holds it to its scopes, and no other method of the interface.
	require.NoError(t, enforceAPIKeyScopePolicy(f.apiKey(f.member, "users:read"), business.HostIssueMethod))
	require.Equal(t, codes.PermissionDenied, status.Code(enforceAPIKeyScopePolicy(f.apiKey(f.member), business.HostIssueMethod)))
	require.Equal(t, codes.PermissionDenied, status.Code(enforceAPIKeyScopePolicy(f.apiKey(f.member, "*:*"), "/host.authority.v1.AuthorityService/Delegate")))
}

// A token bound to one solution's resource is issued the person's capability
// at that solution, and nowhere else: not at another solution the person
// holds a role on, and not without the installation that admitted it.
func TestAResourceBoundTokenIsIssuedThePersonsCapabilityAtItsSolutionAlone(t *testing.T) {
	f := newStatedCredentialFixture(t)
	issued, err := f.issue(f.resourceToken(f.member, f.audit, f.auditInstallation), f.principal(f.audit))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{f.audit + ".log:read", f.audit + ".log:write"}, held(t, issued), "what the person holds there, as for their own session")
	require.Equal(t, f.principal(f.audit), issued.GetContext().GetAudience())
	require.Equal(t, f.member, issued.GetContext().GetOwnerPrincipalId())

	// A token for other's resource, presented for audit: confined elsewhere.
	_, err = f.issue(f.resourceToken(f.member, f.other, f.otherInstalled), f.principal(f.audit))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "confined")
	// A module is no solution's resource.
	_, err = f.issue(f.resourceToken(f.member, f.audit, f.auditInstallation), f.records)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	// No installation stated: the perimeter admitted it without resolving its consent.
	_, err = f.issue(f.resourceToken(f.member, f.audit, ""), f.principal(f.audit))
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	// A person holding no role there is refused as on their own session.
	_, err = f.issue(f.resourceToken(f.outsider, f.audit, f.auditInstallation), f.principal(f.audit))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "holds no role on "+f.audit)
	// A resource this host does not issue.
	foreign := accountsauth.WithVerifiedResourceConfinement(f.resourceToken(f.member, f.audit, f.auditInstallation), "https://elsewhere.example.test/api/solutions/"+f.audit+"/proxy/mcp")
	_, err = f.issue(foreign, f.principal(f.audit))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}

// An impersonated session stays refused, whatever credential carries it: it is
// issued nothing for the person it views as.
func TestAnImpersonatedCredentialIsIssuedNothingAtASolution(t *testing.T) {
	f := newStatedCredentialFixture(t)
	operator := uuid.New()
	for name, kind := range map[string]string{"a session": credentialKindSession, "an API key": credentialKindAPIKey} {
		t.Run(name, func(t *testing.T) {
			ctx := accountsauth.WithVerifiedRequestIdentity(context.Background(), accountsauth.RequestIdentity{
				RealActor: operator, EffectiveSubject: uuid.MustParse(f.member), OrgID: uuid.MustParse(f.org), SessionID: uuid.New()})
			ctx = withScopes(withCredentialKind(ctx, kind), []string{"*:*"})
			_, err := f.issue(ctx, f.principal(f.audit))
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "impersonated")
		})
	}
}

func TestEndedResourceTokenSessionRefusesRetainedCapabilityAndNewIssue(t *testing.T) {
	f := newStatedCredentialFixture(t)
	ctx := f.resourceToken(f.member, f.audit, f.auditInstallation)
	audience := f.principal(f.audit)
	issued, err := f.issue(ctx, audience)
	require.NoError(t, err)
	_, err = f.server.Check(context.Background(), &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: audience})
	require.NoError(t, err)
	f.sessions.sessionFailure = business.ErrNoOpenSession
	_, err = f.server.Check(context.Background(), &authorityv1.CheckRequest{Token: issued.GetToken(), Audience: audience})
	require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	out, err := f.issue(ctx, audience)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	require.Nil(t, out)
}
