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
// These mints are bounded twice. Organization RBAC bounds what a binding
// projects: the administrator holds nothing of the relays' kind until an
// organisation role names it, and is refused by that bound whatever role
// they hold on the page. Once an organisation role names the kind, RBAC
// refuses nothing here, and what decides is the role held on the page's
// installation, read by the core at the binding's target: the grant reaches
// the relay it is addressed to and no sibling, and a role without the grant
// reaches neither. The relays' kind named outright is refused before and
// after, as the core refuses it: an organisation role adds no kind of
// another module to a capability addressed to the page.
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
		refusedOutright(t, err, "the relays' kind named outright")
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
	// caller, and neither does this mint: it is refused as the core refuses
	// it, though an organisation role names the kind and the admin role's
	// grant is addressed to the relay. The kind is carried by naming the
	// binding that reaches the relay, and naming it beside that binding asks
	// nothing more.
	t.Run("the relays' kind named outright is refused; the binding to the relay carries it", func(t *testing.T) {
		_, err := named()
		refusedOutright(t, err, "an organisation role names the kind, and the page serves none of it")
		_, err = start([]*gen.WorkContextScope{ask}, "other")
		denied(t, err, "named beside the binding to the sibling, which projects nothing")
		through, err := start([]*gen.WorkContextScope{ask}, "relay")
		require.NoError(t, err, "named beside the binding to the relay the grant is addressed to")
		require.Equal(t, []string{turn}, carriedKinds(t, through))
		_, err = exchangeAs("other", through.GetToken())
		denied(t, err, "the page's exchange through its binding to the sibling")
		child, err := exchangeAs("relay", through.GetToken())
		require.NoError(t, err, "the page's exchange through its binding to the relay the grant is addressed to")
		require.Equal(t, []string{turn}, carriedKinds(t, child))
	})
}

// testAudienceGrantNeedsItsRole: the organisation role that names the relays'
// kind reaches neither relay through the page for a person whose role on the
// page's installation holds no grant there: no projection, and no kind named
// outright, so no capability of theirs carries it for the page to exchange.
// It runs after testAudienceGrantOwnSession, on the page it delivered, as a
// person who holds the page's user role and the same organisation role.
func testAudienceGrantNeedsItsRole(t *testing.T, conn grpc.ClientConnInterface, admin, user context.Context, userID string) {
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
	// Named outright it is refused as well, so no capability of this person
	// carrying the relays' kind exists for the page to exchange.
	_, err = start([]*gen.WorkContextScope{{ResourceKind: turn, Actions: []string{"run"}}})
	refusedOutright(t, err, "the relays' kind named outright on a role that holds nothing there")
}
