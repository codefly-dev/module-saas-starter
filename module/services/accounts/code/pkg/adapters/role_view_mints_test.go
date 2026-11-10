package adapters

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
	policyv1 "accounts/pkg/gen/saas/policy/v1"
	"accounts/pkg/infra"
)

// How each entry point that mints, derives or decides a person's authority
// treats a session that views as some of its roles. Every method of the
// services that hold such a call is listed with its decision, so a method added
// without one fails this test: the view is never ignored by a path nobody
// thought about.
//
//   - core: decided by the shared authority core, which reads every acting
//     person through their session's role view (authority-core personAs and
//     personOfClaims), so the view holds.
//   - refused: a mint decided by organization RBAC, which a view does not
//     narrow; refused by name in a role view (authorizeOwner, role_view_guard.go).
//   - derives: re-signs or narrows a capability already held, never beyond it,
//     so the view that bounded it still does.
//   - none: mints and decides nothing for a person's session.
const (
	core    = "core"
	refused = "refused"
	derives = "derives"
	none    = "none"
)

var personMintEntryPoints = map[string]map[string]string{
	"WorkContextService": {
		"StartTask": refused, "StartRootSession": refused, "ExchangeAudience": refused, "StartChildSession": refused,
		"RenewWorkContext": derives, "StartInstallationTask": none,
		"CheckAuthorizationRevision": none, "AuthorizeEvidenceRead": none, "ConsumeSingleUse": none, "AdmitWorkloadOperation": none,
	},
	"AuthorityService": {
		"Issue": core, "Check": core, "ListResources": core, "Delegate": core, "RevokeDelegation": core, "GetDelegation": core,
		"AttachResource": core, "DetachResource": core, "AuthorizeTransfer": core,
		"Keys": none, "RegisterAdmissionKey": none,
	},
	"InstallationService": {
		"Current": core, "Install": core, "Review": core, "GrantRole": core, "RevokeRole": core, "ListRoleGrants": core, "List": core, "ListHeld": core,
	},
	"OfferService":   {"ListOffers": core, "ReviewOffer": core},
	"SessionService": {"SignIn": none, "Current": none, "OpenRoleView": core, "ListFixturePeople": none},
}

func TestEveryPersonMintEntryPointHonoursOrRefusesARoleView(t *testing.T) {
	for name, desc := range map[string]*grpc.ServiceDesc{
		"WorkContextService": &gen.WorkContextService_ServiceDesc, "AuthorityService": &authorityv1.AuthorityService_ServiceDesc,
		"InstallationService": &installationv1.InstallationService_ServiceDesc, "OfferService": &installationv1.OfferService_ServiceDesc,
		"SessionService": &sessionv1.SessionService_ServiceDesc,
	} {
		decided := personMintEntryPoints[name]
		var served []string
		for _, m := range desc.Methods {
			served = append(served, m.MethodName)
			if decided[m.MethodName] == "" {
				t.Errorf("%s/%s has no decision about a role view: classify it core, refused, derives or none in personMintEntryPoints, and test it if it is refused", name, m.MethodName)
			}
		}
		for method := range decided {
			if !slices.Contains(served, method) {
				t.Errorf("%s/%s is classified but not served: remove it from personMintEntryPoints", name, method)
			}
		}
	}
}

// testRoleViewRefusesOrganizationMints opens a role view for the person person
// calls as, on an installation they hold a role on, then calls each mint
// listed as refused with the view session's bearer.
func testRoleViewRefusesOrganizationMints(t *testing.T, conn grpc.ClientConnInterface, person context.Context, store *infra.PostgresStore) {
	held, err := installationv1.NewInstallationServiceClient(conn).ListHeld(person, &installationv1.ListHeldRequest{OrganizationId: fixtureOrganization})
	require.NoError(t, err)
	require.NotEmpty(t, held.GetInstallations(), "the person holds a role somewhere")
	one := held.GetInstallations()[0]
	view, err := sessionv1.NewSessionServiceClient(conn).OpenRoleView(person, &sessionv1.OpenRoleViewRequest{InstallationId: one.GetInstallation().GetId(), Roles: one.GetRoles()[:1]})
	require.NoError(t, err)
	require.NotEmpty(t, view.GetBearer())
	require.Equal(t, []string{one.GetInstallation().GetId()}, []string{view.GetRoleView()[0].GetInstallationId()}, "the session says what it views")
	viewing := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+view.GetBearer())
	contexts := gen.NewWorkContextServiceClient(conn)

	_, err = contexts.StartTask(viewing, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: "records",
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-entries", Actions: []string{"read"}}}})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "this session views as")
	require.Contains(t, status.Convert(err).Message(), "use the session the view was opened from")

	// Every accounts operation roleViewAdmits does not name is refused in a view,
	// by name and before its handler runs: API keys, approvals, delegations,
	// installs, platform and member administration. The set is computed from the
	// reviewed policies, so a new operation is refused without being remembered here.
	refusedInView := 0
	for _, policy := range business.RPCPolicies() {
		if !strings.HasPrefix(policy.FullMethod, "/saas.accounts.v1.") || roleViewAdmits(policy.FullMethod, policy, true) ||
			policy.MethodPolicy.GetExposure() != policyv1.Exposure_EXPOSURE_AUTHENTICATED {
			continue // internal operations are reached with a workload credential, never a person's session
		}
		refusedInView++
		err := conn.Invoke(viewing, policy.FullMethod, &emptypb.Empty{}, &emptypb.Empty{})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", policy.FullMethod, err)
		require.Contains(t, status.Convert(err).Message(), "this session views as", policy.FullMethod)
	}
	require.Greater(t, refusedInView, 100, "a view refuses most of the accounts surface by default")

	// Switching organisation re-signs the access token for another organisation, and
	// that token would not be a view's: a view refuses it by name rather than
	// trusting the core's session-keyed record to survive it.
	_, err = gen.NewAuthServiceClient(conn).SwitchOrganization(viewing, &gen.SwitchOrganizationRequest{OrganizationId: globexOrganization})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "this session views as")

	testRoleViewHoldsNoPlatformRole(t, conn, person, view, store)
	testRoleViewHoldsNoOrganizationRole(t, conn, person, viewing)

	// The session it was opened from mints as before, and Current says it views nothing.
	_, err = contexts.StartTask(person, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(), Audience: "records",
		AuthorityScopes: []*gen.WorkContextScope{{ResourceKind: "example-entries", Actions: []string{"read"}}}})
	require.NoError(t, err)
	current, err := sessionv1.NewSessionServiceClient(conn).Current(person, &sessionv1.CurrentRequest{})
	require.NoError(t, err)
	require.Empty(t, current.GetRoleView())

	// A view session is not a client's: it cannot open a view of an impersonating or a forged parent either.
	_, err = sessionv1.NewSessionServiceClient(conn).OpenRoleView(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer not-a-token"),
		&sessionv1.OpenRoleViewRequest{InstallationId: one.GetInstallation().GetId(), Roles: one.GetRoles()[:1]})
	require.Error(t, err)
}

// The closed list of what a view may do must stay true to the descriptors: every
// named write is a real, resolvable, non-restricted operation, every admitted
// operation that is not a declared read is on the list, and nothing admitted as a
// read has the name of a mutation (a mislabelled descriptor would otherwise open
// a hole for ever).
func TestRoleViewAdmitsOnlyReadsLoginFlowsAndNamedPersonWrites(t *testing.T) {
	mutating := regexp.MustCompile(`^(Create|Update|Delete|Decide|Install|Uninstall|Grant|Revoke|Add|Remove|Set|Enable|Disable|Share|Transfer|Narrow|Accept|Register|Put|Upsert|Sync|Start|Issue|Rotate|Reset|Mint|Approve|Request)`)
	for short := range roleViewWrites {
		policy, ok := business.LookupRPCPolicy("/saas.accounts.v1." + short)
		require.True(t, ok, "%s is on roleViewWrites but is not an accounts operation", short)
		require.False(t, business.ImpersonationForbidden(policy), "%s is on roleViewWrites but is a restricted operation", short)
	}
	for short := range roleViewReadsThatWrite {
		_, ok := business.LookupRPCPolicy("/saas.accounts.v1." + short)
		require.True(t, ok, "%s is on roleViewReadsThatWrite but is not an accounts operation", short)
	}
	for _, policy := range business.RPCPolicies() {
		if !strings.HasPrefix(policy.FullMethod, "/saas.accounts.v1.") || !roleViewAdmits(policy.FullMethod, policy, true) {
			continue
		}
		short := strings.TrimPrefix(policy.FullMethod, "/saas.accounts.v1.")
		if roleViewWrites[short] || policy.MethodPolicy.GetRateLimit() != policyv1.RateLimitClass_RATE_LIMIT_CLASS_STANDARD_READ {
			continue // login flows carry no session authority; named writes are on the list
		}
		name := short[strings.LastIndex(short, "/")+1:]
		require.False(t, mutating.MatchString(name),
			"%s is admitted in a role view as a read or login flow but is named like a mutation: fix its descriptor, or add it to roleViewReadsThatWrite", short)
	}
}

// testRoleViewHoldsNoPlatformRole gives the person a platform role, as a platform
// administrator holds one, and asserts the lookup every platform gate shares
// (platformRole) returns it from the session the view was opened from and
// nothing from the view; then that a core that cannot be read answers an error
// rather than the stored role. It fails when platformRole ignores the view.
func testRoleViewHoldsNoPlatformRole(t *testing.T, conn grpc.ClientConnInterface, person context.Context, view *sessionv1.Session, store *infra.PostgresStore) {
	t.Helper()
	ctx := context.Background()
	personID := view.GetPersonId()
	require.NoError(t, store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.GrantPlatformRole(ctx, personID, "super_admin", personID)
	}))
	t.Cleanup(func() {
		_ = store.WithControlPlane(context.Background(), func(ctx context.Context) error { return store.RevokePlatformRole(ctx, personID) })
	})

	parent, err := sessionv1.NewSessionServiceClient(conn).Current(person, &sessionv1.CurrentRequest{})
	require.NoError(t, err)
	identityOf := func(base context.Context, session string) context.Context {
		sessionID, err := uuid.Parse(session)
		require.NoError(t, err)
		subject, err := uuid.Parse(personID)
		require.NoError(t, err)
		return accountsauth.WithVerifiedRequestIdentity(base, accountsauth.RequestIdentity{RealActor: subject, EffectiveSubject: subject, SessionID: sessionID})
	}

	outside, err := platformRole(identityOf(ctx, parent.GetSessionId()), personID)
	require.NoError(t, err)
	require.Equal(t, "super_admin", outside, "outside the view the person keeps the role they hold")

	inside, err := platformRole(identityOf(ctx, view.GetSessionId()), personID)
	require.NoError(t, err)
	require.Empty(t, inside, "inside the view the role is empty, whatever the store says")

	unreadable, cancel := context.WithCancel(ctx)
	cancel()
	role, err := platformRole(identityOf(unreadable, view.GetSessionId()), personID)
	require.Error(t, err, "a core that cannot be read is an error, never the stored role")
	require.Empty(t, role)
}

// testRoleViewHoldsNoOrganizationRole: the person is an organization administrator.
// Outside the view they list the organization's invitations and installations;
// inside it they are a viewer and read what a viewer reads. These reads are
// admitted in a view (they are declared reads) and are decided by the person's
// organization role, so before the view cleared that role an administrator
// "viewing as a viewer" saw what no viewer can. It fails when requireOrgAdmin
// ignores the view.
func testRoleViewHoldsNoOrganizationRole(t *testing.T, conn grpc.ClientConnInterface, person, viewing context.Context) {
	t.Helper()
	invitations := gen.NewInvitationServiceClient(conn)
	installations := gen.NewInstallationServiceClient(conn)

	_, err := invitations.ListInvitations(person, &gen.ListInvitationsRequest{OrgId: fixtureOrganization})
	require.NoError(t, err, "outside the view the administrator lists the organization's invitations")
	_, err = installations.ListOrganizationInstallations(person, &gen.ListOrganizationInstallationsRequest{OrgId: fixtureOrganization})
	require.NoError(t, err, "outside the view the administrator lists the organization's installations")

	_, err = invitations.ListInvitations(viewing, &gen.ListInvitationsRequest{OrgId: fixtureOrganization})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "inside the view: %v", err)
	require.Contains(t, status.Convert(err).Message(), "views as")
	_, err = installations.ListOrganizationInstallations(viewing, &gen.ListOrganizationInstallationsRequest{OrgId: fixtureOrganization})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "inside the view: %v", err)
	require.Contains(t, status.Convert(err).Message(), "views as")
}
