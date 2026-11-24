package adapters

import (
	"context"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// testAudienceGrantOwnSession drives accounts' own person Work Context mints
// (StartTask, a module's binding exchange, the owner's ExchangeAudience and
// StartRootSession) at a role-gated module whose admin role holds a grant
// addressed to one relay, beside a sibling relay serving the same kind over
// another binding. The shared core issues that grant at the relay it is
// addressed to and nowhere else (the suite's reachThroughConsent); these
// mints are a second issuance path the suite never drives.
//
// These mints are bounded twice. Organization RBAC bounds every kind the
// audience does not itself serve: the administrator holds nothing of the
// relays' kind until an organisation role names it, and is refused by that
// bound whatever role they hold on the page. Once an organisation role names
// the kind, RBAC refuses nothing here, and what decides is the role held on
// the page's installation, read by the core at the binding's target: the
// grant reaches the relay it is addressed to and no sibling, and a role
// without the grant reaches neither.
//
// admin is the administrator's session and adminID their person id; seat
// grants them a role on a module page's installation.
func testAudienceGrantOwnSession(t *testing.T, conn grpc.ClientConnInterface, admin context.Context, adminID string, credential func(string) string, deliver func(conformanceModule), page *solutionhost.Frontend, seat func(target, role string)) {
	const (
		module  = "conformance-grant-page"
		relay   = "conformance-grant-relay"
		sibling = "conformance-grant-other-relay"
		turn    = relay + ".turn"
	)
	run := wireScope(turn, "run")
	note := wireScope(module+".note", "read")
	for _, alias := range []string{relay, sibling} {
		deliver(conformanceModule{alias: alias, ceilings: []*authorityv1.WorkScopeV1{run}})
	}
	invoke := map[string][]string{"invoke": {turn + ":run"}}
	deliver(conformanceModule{alias: module, ceilings: []*authorityv1.WorkScopeV1{note}, page: page, installationRole: domain.NoInstallationRole,
		// The admin runs turns at the relay, and nowhere else; the user holds
		// the page's notes alone.
		roles:     map[string][]*authorityv1.WorkScopeV1{"admin": {note}, "user": {note}},
		audiences: map[string]map[string][]*authorityv1.WorkScopeV1{"admin": {relay: {run}}},
		bindings: []solutionhost.AuthorityBinding{
			{ID: "relay", Audience: relay, Operations: invoke},
			{ID: "other", Audience: sibling, Operations: invoke},
		}})

	contexts := gen.NewWorkContextServiceClient(conn)
	ask := &gen.WorkContextScope{ResourceKind: turn, Actions: []string{"run"}}
	start := func(scopes []*gen.WorkContextScope, bindings ...string) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(admin, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: module, AuthorityScopes: scopes, OperationBindings: bindings})
	}
	named := func() (*gen.IssuedWorkContext, error) { return start([]*gen.WorkContextScope{ask}) }
	exchangeAs := func(binding, parent string) (*gen.IssuedWorkContext, error) {
		return moduleExchange(t, credential, module, binding, parent)
	}
	denied := func(t *testing.T, err error, why string) {
		t.Helper()
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", why, err)
	}
	bindings := []string{"relay", "other"}

	seat(module, domain.AdminRole)
	t.Run("organization RBAC bounds the relays' kind, whatever role is held on the page", func(t *testing.T) {
		for _, binding := range bindings {
			_, err := start(nil, binding)
			denied(t, err, "a projection through binding "+binding+" of a kind no organisation role names")
			require.Contains(t, status.Convert(err).Message(), "holds nothing under operation binding")
		}
		_, err := named()
		denied(t, err, "the relays' kind named outright")
		require.Contains(t, status.Convert(err).Message(), "beyond what the owner holds")
	})

	// An organisation role now names the relays' kind: RBAC refuses nothing
	// below, and the role on the page's installation decides.
	permissions := gen.NewPermissionServiceClient(conn)
	created, err := permissions.CreateRole(admin, &gen.CreateRoleRequest{OrgId: fixtureOrganization, Name: "Relay turn operator",
		Permissions: []*gen.Permission{{Resource: turn, Action: "run"}}})
	require.NoError(t, err)
	assigned := &gen.AssignRoleRequest{SubjectId: adminID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: created.GetRole().GetId(), OrgId: fixtureOrganization}
	_, err = permissions.AssignRole(admin, assigned)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = permissions.RevokeRole(admin, &gen.RevokeRoleRequest{SubjectId: adminID, RoleId: assigned.GetRoleId(), OrgId: fixtureOrganization})
	})

	t.Run("the grant is held at the relay it is addressed to", func(t *testing.T) {
		through, err := start(nil, "relay")
		require.NoError(t, err, "a projection through the binding to the relay the grant is addressed to")
		require.Equal(t, []string{turn}, carriedKinds(t, through))
		child, err := exchangeAs("relay", through.GetToken())
		require.NoError(t, err, "the page's exchange through its binding to that relay")
		require.Equal(t, []string{turn}, carriedKinds(t, child))
	})

	t.Run("the same scope at a sibling relay over another binding is refused", func(t *testing.T) {
		_, err := start(nil, "other")
		denied(t, err, "a projection through the binding to the sibling")
		through, err := start(nil, "relay")
		require.NoError(t, err)
		_, err = exchangeAs("other", through.GetToken())
		denied(t, err, "the page's exchange through its binding to the sibling, of a capability projected for the relay")
		// The owner's own exchange and root session reach neither relay: a
		// module that declares no roles holds nothing for a person's own
		// session, whatever the capability carries.
		for _, audience := range []string{relay, sibling} {
			_, err := contexts.ExchangeAudience(admin, &gen.ExchangeWorkContextAudienceRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: through.GetToken(), Audience: audience, AttenuatedScopes: []*gen.WorkContextScope{ask}})
			refusedAtNoRoles(t, err, audience)
			_, err = contexts.StartRootSession(admin, &gen.StartRootSessionWorkContextRequest{OrgId: fixtureOrganization,
				ParentWorkContextToken: through.GetToken(), SessionId: uuid.NewString(), Audience: audience})
			refusedAtNoRoles(t, err, audience)
		}
	})

	// The relays' kind named outright on a capability addressed to the page.
	// The core never signs an audience grant onto a token addressed to its
	// caller. Here a kind the audience does not serve is inert at it and
	// bounded by organization RBAC alone, as every such kind is on these
	// mints (an exchange attenuates from its parent, so a parent carries
	// what its exchanges will): it is admitted, and carried only where the
	// role's grant is addressed.
	t.Run("the relays' kind named outright is inert at the page and carried only to the relay", func(t *testing.T) {
		outright, err := named()
		require.NoError(t, err, "bounded by organization RBAC, which names the kind")
		require.Equal(t, []string{turn}, carriedKinds(t, outright))
		_, err = exchangeAs("other", outright.GetToken())
		denied(t, err, "the page's exchange through its binding to the sibling")
		child, err := exchangeAs("relay", outright.GetToken())
		require.NoError(t, err, "the page's exchange through its binding to the relay the grant is addressed to")
		require.Equal(t, []string{turn}, carriedKinds(t, child))
	})
}

// testAudienceGrantNeedsItsRole: the organisation role that names the relays'
// kind reaches neither relay through the page for a person whose role on the
// page's installation holds no grant there. It runs after
// testAudienceGrantOwnSession, on the page it delivered, as a person who
// holds the page's user role and the same organisation role.
func testAudienceGrantNeedsItsRole(t *testing.T, conn grpc.ClientConnInterface, admin, user context.Context, userID string, credential func(string) string) {
	const (
		module = "conformance-grant-page"
		turn   = "conformance-grant-relay.turn"
	)
	permissions := gen.NewPermissionServiceClient(conn)
	roles, err := permissions.ListRoles(admin, &gen.ListRolesRequest{OrgId: fixtureOrganization})
	require.NoError(t, err)
	var operator string
	for _, role := range roles.GetRoles() {
		if role.GetName() == "Relay turn operator" {
			operator = role.GetId()
		}
	}
	require.NotEmpty(t, operator, "the organisation role over the relays' kind")
	_, err = permissions.AssignRole(admin, &gen.AssignRoleRequest{SubjectId: userID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: operator, OrgId: fixtureOrganization})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = permissions.RevokeRole(admin, &gen.RevokeRoleRequest{SubjectId: userID, RoleId: operator, OrgId: fixtureOrganization})
	})
	contexts := gen.NewWorkContextServiceClient(conn)
	start := func(scopes []*gen.WorkContextScope, bindings ...string) (*gen.IssuedWorkContext, error) {
		return contexts.StartTask(user, &gen.StartTaskWorkContextRequest{OrgId: fixtureOrganization, TaskId: uuid.NewString(),
			Audience: module, AuthorityScopes: scopes, OperationBindings: bindings})
	}
	for _, binding := range []string{"relay", "other"} {
		_, err := start(nil, binding)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "a projection through binding %s on a role that holds nothing there: %v", binding, err)
	}
	outright, err := start([]*gen.WorkContextScope{{ResourceKind: turn, Actions: []string{"run"}}})
	require.NoError(t, err, "inert at the page, bounded by organization RBAC")
	for _, binding := range []string{"relay", "other"} {
		_, err := moduleExchange(t, credential, module, binding, outright.GetToken())
		require.Equal(t, codes.PermissionDenied, status.Code(err), "the page's exchange through binding %s of a kind the person's role does not hold there: %v", binding, err)
	}
}
