//go:build !pure

package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"accounts/pkg/adapters"
	authcore "accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The access matrix: the operations people administer this host with, each
// called through its real handler on the real store by five people, and held
// to who the contract admits. No host runs.
//
//	the platform administrator  holds super_admin, and is a member of no
//	                            organization but their own
//	Dana                        administers Acme (admin, not its owner)
//	Carol                       is a member of Acme
//	Bob                         is a member of another organization, Globex
//	a signed-out visitor        carries no identity
//
// Everyone signed in carries a recent second factor, so no row is refused for
// the factor: what is left is who they are.
//
// A cell is admitted (the call returns what it was asked for), refused
// (PermissionDenied) or, for the visitor, Unauthenticated. This is the
// handler layer: a permission a procedure declares is enforced one step
// earlier, at admission, which only pkg/adapters can drive
// (TestRolePlanHoldsEveryProcedureForEveryPerson). Those cells are named in
// `atAdmission` and left to that test rather than asserted weaker here.

const (
	theOperator = "the platform administrator"
	dana        = "Dana"
	carol       = "Carol"
	bob         = "Bob"
	theVisitor  = "a signed-out visitor"
)

var accessMatrixPeople = []string{theOperator, dana, carol, bob, theVisitor}

type accessWorld struct {
	acme, globex          string
	founder               string
	people                map[string]string // person -> user id
	sessions              map[string]string // person -> organization their session is in
	emptyTeam, carolsTeam string
	carolsSession         string
	stranger              string // a registered identity that is a member of nothing in Acme
}

func newAccessWorld(t *testing.T) *accessWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &accessWorld{people: map[string]string{}, sessions: map[string]string{}}

	w.founder, w.acme = mustUserAndOrg(t, testCtx, "founder@acme.example.com", "matrix-founder", "Acme")
	w.people[dana] = mustUser(t, "dana@acme.example.com", "matrix-dana")
	require.NoError(t, testService.AddOrgMember(testCtx, w.founder, &gen.AddOrgMemberRequest{
		OrgId: w.acme, UserId: w.people[dana], Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
	w.people[carol] = mustMember(t, w.founder, w.acme, "carol@acme.example.com", "matrix-carol")
	w.people[bob], w.globex = mustUserAndOrg(t, testCtx, "bob@globex.example.com", "matrix-bob", "Globex")
	operatorOrg := ""
	w.people[theOperator], operatorOrg = mustUserAndOrg(t, testCtx, "operator@example.com", "matrix-operator", "Platform Operations")
	grantPlatformRole(t, w.people[theOperator], "super_admin", w.people[theOperator])
	w.stranger = mustUser(t, "newcomer@example.com", "matrix-newcomer")

	w.sessions = map[string]string{theOperator: operatorOrg, dana: w.acme, carol: w.acme, bob: w.globex}

	empty, err := testService.CreateTeam(testCtx, w.founder, &gen.CreateTeamRequest{OrgId: w.acme, Name: "platform"})
	require.NoError(t, err)
	w.emptyTeam = empty.Team.Id
	hers, err := testService.CreateTeam(testCtx, w.founder, &gen.CreateTeamRequest{OrgId: w.acme, Name: "support"})
	require.NoError(t, err)
	w.carolsTeam = hers.Team.Id
	require.NoError(t, testService.AddTeamMember(testCtx, w.founder, &gen.AddTeamMemberRequest{
		TeamId: w.carolsTeam, UserId: w.people[carol], Role: gen.TeamRole_TEAM_ROLE_MEMBER,
	}))

	session := uuid.New()
	now := time.Now()
	require.NoError(t, testSessionStore.Insert(testCtx, &authcore.SessionRecord{
		ID:              session,
		UserID:          uuid.MustParse(w.people[carol]),
		FamilyID:        uuid.New(),
		RefreshHash:     []byte("matrix-carol-refresh-" + session.String()),
		AuthenticatedAt: now,
		IdleExpiresAt:   now.Add(time.Hour),
		ExpiresAt:       now.Add(2 * time.Hour),
	}))
	w.carolsSession = session.String()
	return w
}

// as is the request context one person calls with.
func (w *accessWorld) as(person string) context.Context {
	if person == theVisitor {
		return testCtx
	}
	return callerWithRecentFactor(testCtx, w.people[person], w.sessions[person])
}

type accessRow struct {
	operation string
	// decidedBy is the gate in pkg/adapters that decides the row.
	decidedBy string
	call      func(ctx context.Context, w *accessWorld) error
	admits    []string
	// atAdmission names the people whose cell the declared permission
	// decides before the handler runs; this layer does not assert them.
	atAdmission map[string]string
	// changes marks a call that alters what a later caller would find, so
	// each person meets a fresh world.
	changes bool
}

func accessMatrix() []accessRow {
	users := &adapters.UserServer{}
	orgs := &adapters.OrgServer{}
	teams := &adapters.TeamServer{}
	platform := &adapters.PlatformAdminServer{}
	perms := &adapters.PermServer{}
	keys := &adapters.APIKeyServer{}
	audit := &adapters.AuditServer{}
	uuidOf := func(id string) *gen.GetUserRequest {
		return &gen.GetUserRequest{Identifier: &gen.GetUserRequest_Uuid{Uuid: id}}
	}

	return []accessRow{
		// ---- people: listing, reading, changing, suspending, deleting ----
		{operation: "list every user", decidedBy: "UserServer.ListUsers: requirePlatformAdmin",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.ListUsers(ctx, &gen.ListUsersRequest{PageSize: 10})
				return err
			}},
		{operation: "search users", decidedBy: "PlatformAdminServer.SearchUsers: requirePlatformRole(support)",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.SearchUsers(ctx, &gen.SearchUsersRequest{Query: "carol", PageSize: 10})
				return err
			}},
		{operation: "read Carol's account", decidedBy: "UserServer.GetUser: userDataIdentity (self, or requirePlatformAdmin)",
			admits: []string{theOperator, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.GetUser(ctx, uuidOf(w.people[carol]))
				return err
			}},
		{operation: "look an account up by email", decidedBy: "UserServer.GetUser: requirePlatformAdmin",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.GetUser(ctx, &gen.GetUserRequest{Identifier: &gen.GetUserRequest_Email{Email: "carol@acme.example.com"}})
				return err
			}},
		{operation: "list Carol's sign-in identities", decidedBy: "UserServer.ListUserIdentities: userDataIdentity",
			admits: []string{theOperator, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.ListUserIdentities(ctx, &gen.ListUserIdentitiesRequest{UserUuid: w.people[carol]})
				return err
			}},
		{operation: "change Carol's profile", decidedBy: "UserServer.UpdateUser: userDataIdentity",
			admits: []string{theOperator, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.UpdateUser(ctx, &gen.UpdateUserRequest{
					Uuid:       w.people[carol],
					User:       &gen.User{Uuid: w.people[carol], PrimaryEmail: "carol@acme.example.com", Profile: map[string]string{"name": "Carol"}},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"profile"}},
				})
				return err
			}},
		{operation: "link a sign-in identity to Carol's account", decidedBy: "UserServer.AddIdentity: the caller's own account only",
			admits: []string{carol}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.AddIdentity(ctx, &gen.AddIdentityRequest{
					UserUuid: w.people[carol],
					Identity: &gen.UserIdentity{Provider: "google", ProviderId: "matrix-carol-google", ProviderEmail: "carol@acme.example.com"},
				})
				return err
			}},
		{operation: "suspend Carol", decidedBy: "PlatformAdminServer.SuspendUser: requirePlatformRole(super_admin)",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.SuspendUser(ctx, &gen.SuspendUserRequest{UserId: w.people[carol], Reason: "matrix"})
				return err
			}},
		{operation: "lift a suspension", decidedBy: "PlatformAdminServer.UnsuspendUser: requirePlatformRole(super_admin)",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.UnsuspendUser(ctx, &gen.UnsuspendUserRequest{UserId: w.people[carol]})
				return err
			}},
		{operation: "delete Carol's account", decidedBy: "UserServer.DeleteUser: userDataIdentity",
			admits: []string{theOperator, carol}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := users.DeleteUser(ctx, uuidOf(w.people[carol]))
				return err
			}},

		// ---- sessions and impersonation ----
		{operation: "list Carol's sessions", decidedBy: "PlatformAdminServer.ListActiveSessions: requirePlatformRole(support)",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.ListActiveSessions(ctx, &gen.ListActiveSessionsRequest{UserId: w.people[carol], PageSize: 10})
				return err
			}},
		{operation: "sign Carol out of a session", decidedBy: "PlatformAdminServer.RevokeSession: requirePlatformRole(support)",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.RevokeSession(ctx, &gen.RevokeSessionRequest{SessionId: w.carolsSession, Reason: "matrix"})
				return err
			}},
		{operation: "act as Carol", decidedBy: "PlatformAdminServer.ImpersonateUser: requirePlatformRole(support)",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.ImpersonateUser(ctx, &gen.ImpersonateUserRequest{UserId: w.people[carol], Reason: "ticket SUP-1001"})
				return err
			}},

		// ---- the platform ----
		{operation: "list every organization", decidedBy: "PlatformAdminServer.ListAllOrganizations: requirePlatformRole(support)",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.ListAllOrganizations(ctx, &gen.ListAllOrganizationsRequest{PageSize: 10})
				return err
			}},
		{operation: "read Acme's roster from the platform console", decidedBy: "PlatformAdminServer.GetOrganizationRoster: requirePlatformRole(support)",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.GetOrganizationRoster(ctx, &gen.GetOrganizationRosterRequest{OrgId: w.acme})
				return err
			}},
		{operation: "list the platform's administrators", decidedBy: "PlatformAdminServer.ListPlatformAdmins: requirePlatformRole(super_admin)",
			admits: []string{theOperator},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.ListPlatformAdmins(ctx, &gen.ListPlatformAdminsRequest{})
				return err
			}},
		{operation: "give Carol a platform role", decidedBy: "PlatformAdminServer.GrantPlatformRole: requirePlatformRole(super_admin)",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.GrantPlatformRole(ctx, &gen.GrantPlatformRoleRequest{UserId: w.people[carol], PlatformRole: "support"})
				return err
			}},

		// ---- entitlements ----
		{operation: "read Acme's entitlements", decidedBy: "PlatformAdminServer.GetOrgEntitlements: requireOrgMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.GetOrgEntitlements(ctx, &gen.GetOrgEntitlementsRequest{OrgId: w.acme})
				return err
			}},
		{operation: "override one of Acme's entitlements", decidedBy: "PlatformAdminServer.OverrideEntitlement: requirePlatformAdmin",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := platform.OverrideEntitlement(ctx, &gen.OverrideEntitlementRequest{
					OrgId: w.acme, Feature: "seats", LimitValue: 50, Reason: "matrix",
				})
				return err
			}},

		// ---- the organization and its members ----
		{operation: "read Acme", decidedBy: "OrgServer.GetOrganization: requireOrgMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.GetOrganization(ctx, &gen.GetOrganizationRequest{Id: w.acme})
				return err
			}},
		{operation: "list Acme's members", decidedBy: "OrgServer.ListMembers: requireOrgMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.ListMembers(ctx, &gen.ListOrgMembersRequest{OrgId: w.acme})
				return err
			}},
		{operation: "rename Acme", decidedBy: "OrgServer.UpdateOrganization: requireOrgAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.UpdateOrganization(ctx, &gen.UpdateOrganizationRequest{OrgId: w.acme, Name: "Acme Corp", Slug: "matrix-founder-org"})
				return err
			}},
		{operation: "add a member to Acme", decidedBy: "OrgServer.AddMember: requireOrgAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.AddMember(ctx, &gen.AddOrgMemberRequest{OrgId: w.acme, UserId: w.stranger, Role: gen.OrgRole_ORG_ROLE_MEMBER})
				return err
			}},
		{operation: "remove Carol from Acme", decidedBy: "OrgServer.RemoveMember: requireOrgAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.RemoveMember(ctx, &gen.RemoveOrgMemberRequest{OrgId: w.acme, UserId: w.people[carol]})
				return err
			}},
		{operation: "delete Acme", decidedBy: "OrgServer.DeleteOrganization: requireOrgOwner",
			admits: nil, // Dana administers Acme and does not own it
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := orgs.DeleteOrganization(ctx, &gen.DeleteOrganizationRequest{OrgId: w.acme, ConfirmSlug: "matrix-founder-org"})
				return err
			}},

		// ---- teams ----
		{operation: "list Acme's teams", decidedBy: "TeamServer.ListTeams: requireOrgMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.ListTeams(ctx, &gen.ListTeamsRequest{OrgId: w.acme})
				return err
			}},
		{operation: "list a team's members", decidedBy: "TeamServer.ListMembers: requireTeamMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.ListMembers(ctx, &gen.ListTeamMembersRequest{TeamId: w.carolsTeam})
				return err
			}},
		{operation: "create a team", decidedBy: "TeamServer.CreateTeam: requireOrgAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.CreateTeam(ctx, &gen.CreateTeamRequest{OrgId: w.acme, Name: "finance"})
				return err
			}},
		{operation: "rename a team", decidedBy: "TeamServer.UpdateTeam: requireTeamAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.UpdateTeam(ctx, &gen.UpdateTeamRequest{TeamId: w.carolsTeam, Name: "customer support"})
				return err
			}},
		{operation: "add Carol to a team", decidedBy: "TeamServer.AddMember: requireTeamAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.AddMember(ctx, &gen.AddTeamMemberRequest{TeamId: w.emptyTeam, UserId: w.people[carol], Role: gen.TeamRole_TEAM_ROLE_MEMBER})
				return err
			}},
		{operation: "remove Carol from her team", decidedBy: "TeamServer.RemoveMember: requireTeamAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.RemoveMember(ctx, &gen.RemoveTeamMemberRequest{TeamId: w.carolsTeam, UserId: w.people[carol]})
				return err
			}},
		{operation: "delete a team", decidedBy: "TeamServer.DeleteTeam: requireTeamAdmin",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := teams.DeleteTeam(ctx, &gen.DeleteTeamRequest{TeamId: w.emptyTeam})
				return err
			}},

		// ---- roles ----
		{operation: "list Acme's roles", decidedBy: "PermServer.ListRoles: requireOrgMember",
			admits: []string{dana, carol},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := perms.ListRoles(ctx, &gen.ListRolesRequest{OrgId: w.acme})
				return err
			}},
		{operation: "create a role in Acme", decidedBy: "PermServer.CreateRole: requireRoleScope (requireOrgAdmin)",
			admits: []string{dana}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := perms.CreateRole(ctx, &gen.CreateRoleRequest{
					OrgId: w.acme, Name: "matrix reader",
					Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
				})
				return err
			}},
		{operation: "create a role for every organization", decidedBy: "PermServer.CreateRole: requireRoleScope (requirePlatformRole(super_admin))",
			admits: []string{theOperator}, changes: true,
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := perms.CreateRole(ctx, &gen.CreateRoleRequest{
					Name:        "matrix global reader",
					Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
				})
				return err
			}},
		{operation: "read who can reach Acme's collections", decidedBy: "PermServer.ListCollectionAccess: requireOrgAdmin",
			admits: []string{dana},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := perms.ListCollectionAccess(ctx, &gen.ListCollectionAccessRequest{OrgId: w.acme})
				return err
			}},

		// ---- API keys and the audit trail: a declared permission decides the member ----
		{operation: "list Acme's API keys", decidedBy: "admission: api_keys:read; APIKeyServer.ListAPIKeys: requireOrgMember",
			admits: []string{dana}, atAdmission: map[string]string{carol: "api_keys:read"},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := keys.ListAPIKeys(ctx, &gen.ListAPIKeysRequest{OrganizationId: w.acme, PageSize: 10})
				return err
			}},
		{operation: "read Acme's audit trail", decidedBy: "AuditServer.QueryAuditLog: requireAuditRead (audit:read)",
			admits: []string{dana},
			call: func(ctx context.Context, w *accessWorld) error {
				_, err := audit.QueryAuditLog(ctx, &gen.QueryAuditLogRequest{OrgId: w.acme, PageSize: 5})
				return err
			}},
	}
}

func TestAccessMatrix(t *testing.T) {
	for _, row := range accessMatrix() {
		t.Run(row.operation, func(t *testing.T) {
			var world *accessWorld
			admitted := map[string]bool{}
			for _, person := range row.admits {
				admitted[person] = true
			}
			for _, person := range accessMatrixPeople {
				if world == nil || row.changes {
					world = newAccessWorld(t)
				}
				err := row.call(world.as(person), world)
				switch {
				case person == theVisitor:
					require.Equalf(t, codes.Unauthenticated, status.Code(err),
						"%s: %s must be told to sign in (%s): %v", row.operation, person, row.decidedBy, err)
				case row.atAdmission[person] != "":
					// Decided before the handler, by the declared permission.
				case admitted[person]:
					require.NoErrorf(t, err, "%s: %s is admitted (%s)", row.operation, person, row.decidedBy)
				default:
					require.Equalf(t, codes.PermissionDenied, status.Code(err),
						"%s: %s must be refused (%s): %v", row.operation, person, row.decidedBy, err)
				}
			}
		})
	}
}

// An account registered without a profile stores none. The first change to
// it, by its owner, used to panic the server in the merge (a nil map read
// back from JSON null) instead of writing the profile.
func TestAProfileIsWrittenToAnAccountRegisteredWithoutOne(t *testing.T) {
	w := newAccessWorld(t)
	updated, err := (&adapters.UserServer{}).UpdateUser(w.as(carol), &gen.UpdateUserRequest{
		Uuid:       w.people[carol],
		User:       &gen.User{Uuid: w.people[carol], PrimaryEmail: "carol@acme.example.com", Profile: map[string]string{"name": "Carol"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"profile"}},
	})
	require.NoError(t, err)
	require.Equal(t, "Carol", updated.GetProfile()["name"])
}
