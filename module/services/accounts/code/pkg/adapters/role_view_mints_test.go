package adapters

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	installationv1 "accounts/pkg/gen/host/installation/v1"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
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
func testRoleViewRefusesOrganizationMints(t *testing.T, conn grpc.ClientConnInterface, person context.Context) {
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
