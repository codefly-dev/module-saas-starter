//go:build !pure

package business_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	authcore "accounts/pkg/auth"

	"github.com/google/uuid"

	"accounts/pkg/adapters"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tenancyWorld is organization A with an administrator, a member and a role it
// can see; organization B with a team; and an identity that is not a member of
// A, holding a session bound to no organization — the kind of session a write
// naming them in A would reach.
type tenancyWorld struct {
	admin, member, org, role string
	otherTeam                string
	outsider                 string
	outsiderSession          []byte
}

func newTenancyWorld(t *testing.T) *tenancyWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &tenancyWorld{}
	w.admin, w.org = mustUserAndOrg(t, testCtx, "admin@subject-a.test", "subject-a-admin", "Subject A")
	w.member = mustMember(t, w.admin, w.org, "member@subject-a.test", "subject-a-member")
	otherAdmin, otherOrg := mustUserAndOrg(t, testCtx, "admin@subject-b.test", "subject-b-admin", "Subject B")
	team, err := testService.CreateTeam(testCtx, otherAdmin, &gen.CreateTeamRequest{OrgId: otherOrg, Name: "elsewhere"})
	require.NoError(t, err)
	w.otherTeam = team.Team.Id

	w.role = business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id: w.role, Name: "subject reader " + w.role, OrgId: w.org,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "read"}},
		})
	}))

	w.outsider = mustUser(t, "outsider@subject.test", "subject-outsider")
	hash := sha256.Sum256([]byte("outsider-refresh-" + w.outsider))
	w.outsiderSession = hash[:]
	now := time.Now()
	require.NoError(t, testSessionStore.Insert(testCtx, &authcore.SessionRecord{
		UserID:          uuid.MustParse(w.outsider),
		FamilyID:        uuid.New(),
		RefreshHash:     w.outsiderSession,
		AuthenticatedAt: now,
		IdleExpiresAt:   now.Add(time.Hour),
		ExpiresAt:       now.Add(2 * time.Hour),
	}))
	w.requireOutsiderSessionIntact(t)
	return w
}

// requireOutsiderSessionIntact proves the outsider's unbound session was not
// revoked.
func (w *tenancyWorld) requireOutsiderSessionIntact(t *testing.T) {
	t.Helper()
	session, err := testSessionStore.FindByRefreshHash(testCtx, w.outsiderSession)
	require.NoError(t, err)
	require.Nil(t, session.RevokedAt, "a refused write must leave the subject's sessions untouched (revoked: %q)", session.RevokedReason)
}

// awaitBlockedTransaction proves two transactions genuinely contend.
func awaitBlockedTransaction(t *testing.T, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		if err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return testStore.Pool().QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)`).Scan(&waiting)
		}); err != nil {
			return false
		}
		return waiting
	}, 10*time.Second, 20*time.Millisecond, msg)
}

func TestAssignRoleRequiresTheSubjectToBelongToTheOrganization(t *testing.T) {
	w := newTenancyWorld(t)
	server := &adapters.PermServer{}
	caller := storyCallerContext(testCtx, w.admin, w.org)

	// A scoped assignment is the one whose write also touches the subject's
	// sessions; it is refused before it is written.
	_, err := server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.outsider, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		RoleId: w.role, OrgId: w.org, Scope: "docs",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "got %v", err)
	w.requireOutsiderSessionIntact(t)

	_, err = server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.otherTeam, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM, RoleId: w.role, OrgId: w.org,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a team of another organization is refused")

	assignments, err := testService.ListRoleAssignments(testCtx, &gen.ListRoleAssignmentsRequest{OrgId: w.org, SubjectId: w.outsider})
	require.NoError(t, err)
	require.Empty(t, assignments.GetAssignments(), "no refused request wrote an assignment")

	_, err = server.AssignRole(caller, &gen.AssignRoleRequest{
		SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role, OrgId: w.org,
	})
	require.NoError(t, err, "a member of the organization is admitted")
}

// The platform-level path names no organization and is authorised separately;
// the organization check does not apply to it.
func TestAssignRoleKeepsThePlatformLevelPath(t *testing.T) {
	w := newTenancyWorld(t)
	var global string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		roles, err := testStore.ListRoles(ctx, "")
		for _, role := range roles {
			if role.GetBuiltIn() && role.GetOrgId() == "" {
				global = role.GetId()
				break
			}
		}
		return err
	}))
	require.NotEmpty(t, global, "a built-in role exists")
	_, err := testService.AssignRole(testCtx, &gen.AssignRoleRequest{
		SubjectId: w.outsider, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: global,
	})
	require.NoError(t, err)
}

func TestShareRecordRequiresTheSubjectToBelongToTheOrganization(t *testing.T) {
	w := newTenancyWorld(t)
	server := &adapters.PermServer{}
	caller := storyCallerContext(testCtx, w.admin, w.org)
	share := func(subject string, kind gen.SubjectKind) error {
		_, err := server.ShareRecord(caller, &gen.ShareRecordRequest{
			OrgId: w.org, ResourceType: "doc", ResourceId: "record-1",
			SubjectId: subject, SubjectKind: kind, RoleId: w.role,
		})
		return err
	}

	require.Equal(t, codes.PermissionDenied, status.Code(share(w.outsider, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)))
	require.Equal(t, codes.PermissionDenied, status.Code(share(w.otherTeam, gen.SubjectKind_SUBJECT_KIND_TEAM)))
	require.Equal(t, codes.PermissionDenied, status.Code(share(business.NewIDString(), gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)),
		"a subject that does not exist is refused the same way")

	shares, err := testService.ListShares(testCtx, &gen.ListSharesRequest{OrgId: w.org, ResourceType: "doc", ResourceId: "record-1"})
	require.NoError(t, err)
	require.Empty(t, shares.GetShares(), "no refused request wrote a share")

	require.NoError(t, share(w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL), "a member of the organization is admitted")
	w.requireOutsiderSessionIntact(t)
}

// The check holds the membership lock a removal takes, so a removal that
// commits first leaves the waiting write nothing to admit.
func TestSubjectWritesWaitForAConcurrentMembershipRemoval(t *testing.T) {
	writes := map[string]func(w *tenancyWorld) error{
		"AssignRole": func(w *tenancyWorld) error {
			_, err := testService.AssignRole(testCtx, &gen.AssignRoleRequest{
				SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role, OrgId: w.org,
			})
			return err
		},
		"ShareRecord": func(w *tenancyWorld) error {
			_, err := testService.ShareRecord(testCtx, w.admin, &gen.ShareRecordRequest{
				OrgId: w.org, ResourceType: "doc", ResourceId: "record-1",
				SubjectId: w.member, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, RoleId: w.role,
			})
			return err
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			w := newTenancyWorld(t)
			done := make(chan error, 1)
			require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
				if err := testStore.LockOrgMembership(ctx, w.org, w.member); err != nil {
					return err
				}
				if err := testStore.RemoveOrgMember(ctx, w.org, w.member); err != nil {
					return err
				}
				go func() { done <- write(w) }()
				awaitBlockedTransaction(t, "the write should wait on the membership lock the removal holds")
				select {
				case err := <-done:
					t.Fatalf("the write finished while the removal held the membership: %v", err)
				default:
				}
				return nil
			}))
			err := <-done
			require.True(t, errors.Is(err, business.ErrSubjectOutsideOrganization), "got %v", err)
		})
	}
}
