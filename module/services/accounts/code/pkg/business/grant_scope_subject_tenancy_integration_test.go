//go:build !pure

package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// grantScopeWorld is an organization with a registered scope node, a reader
// role and a member, beside a second organization with a team and an admin.
type grantScopeWorld struct {
	admin, member, org, role string
	otherAdmin, otherTeam    string
	path                     string
}

func newGrantScopeWorld(t *testing.T) *grantScopeWorld {
	t.Helper()
	clearData(t)
	adapters.WithService(testService)
	w := &grantScopeWorld{path: "tenancy_root"}
	w.admin, w.org = mustUserAndOrg(t, testCtx, "admin@tenancy.test", "tenancy-admin", "Tenancy A")
	w.member = mustMember(t, w.admin, w.org, "member@tenancy.test", "tenancy-member")
	var otherOrg string
	w.otherAdmin, otherOrg = mustUserAndOrg(t, testCtx, "admin@tenancy-b.test", "tenancy-b-admin", "Tenancy B")
	otherTeam, err := testService.CreateTeam(testCtx, w.otherAdmin, &gen.CreateTeamRequest{OrgId: otherOrg, Name: "elsewhere"})
	require.NoError(t, err)
	w.otherTeam = otherTeam.Team.Id
	w.role = createRole(t, w.org, "reader", "doc", "read")
	require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
		return testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id: business.NewIDString(), OrgId: w.org, ScopePath: w.path, Kind: "collection", Label: "Root",
		})
	}))
	return w
}

func (w *grantScopeWorld) grantTo(subject string, kind gen.SubjectKind) *gen.GrantScopeRequest {
	return &gen.GrantScopeRequest{OrgId: w.org, SubjectId: subject, SubjectKind: kind, ScopePath: w.path, RoleId: w.role}
}

// grantScopeCallerWithFactor is storyCallerContext plus recent second-factor
// evidence, which every privilege-granting mutation on this host requires before
// it runs. Without it these tests exercise the factor gate instead of the rule
// they name.
func grantScopeCallerWithFactor(ctx context.Context, userID, orgID string) context.Context {
	return auth.WithAssurance(
		storyCallerContext(ctx, userID, orgID),
		auth.Assurance{
			AuthenticationMethods: []string{auth.AuthenticationMethodOTP},
			AuthenticatedAt:       time.Now(),
			Level:                 auth.AssuranceLevelAAL2,
			MFAVerifiedAt:         time.Now(),
		},
	)
}

// A scope grant names a subject of the organization it is written in. A subject
// of another organization is refused.
//
// The caller carries a SATISFIED second factor, because a scope grant is a
// privilege-granting mutation and sits behind one (SP-IDENT-10, requireMFA in
// PermServer.GrantScope). That gate runs before the mutation, so an actor without
// a recent factor is refused with `mfa_required` and never reaches the subject at
// all — correct, and it would make this test assert the factor rather than the
// tenancy. Satisfying it is what lets the subject check be the thing under test.
func TestGrantScopeRefusesASubjectOfAnotherOrganization(t *testing.T) {
	w := newGrantScopeWorld(t)
	caller := grantScopeCallerWithFactor(testCtx, w.admin, w.org)

	_, err := (&adapters.PermServer{}).GrantScope(caller, w.grantTo(w.otherTeam, gen.SubjectKind_SUBJECT_KIND_TEAM))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a team of another organization is refused: %v", err)

	_, err = (&adapters.PermServer{}).GrantScope(caller, w.grantTo(w.otherAdmin, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL))
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a principal of another organization is refused: %v", err)

	_, err = (&adapters.PermServer{}).GrantScope(caller, w.grantTo(w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL))
	require.NoError(t, err, "a member of the organization is admitted")
}

// The subject check is part of the grant's own transaction and holds the
// membership lock a removal takes, so a departure and a grant serialize in both
// orders: a grant never lands for a member who had already left.
func TestGrantScopeSerializesWithMembershipRemoval(t *testing.T) {
	t.Run("the removal commits first: the waiting grant finds no member", func(t *testing.T) {
		w := newGrantScopeWorld(t)
		granted := make(chan error, 1)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			if err := testStore.LockOrgMembership(ctx, w.org, w.member); err != nil {
				return err
			}
			if err := testStore.RemoveOrgMember(ctx, w.org, w.member); err != nil {
				return err
			}
			go func() {
				_, err := testService.GrantScope(testCtx, w.admin, w.grantTo(w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL))
				granted <- err
			}()
			requireARowLockWaiter(t, "the grant should wait on the membership lock the removal holds")
			select {
			case err := <-granted:
				t.Fatalf("the grant finished while the removal held the membership: %v", err)
			default:
			}
			return nil
		}))
		err := <-granted
		require.True(t, errors.Is(err, business.ErrSubjectOutsideOrganization), "got %v", err)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("the grant locks first: the removal waits for it", func(t *testing.T) {
		w := newGrantScopeWorld(t)
		removed := make(chan error, 1)
		require.NoError(t, testStore.WithOrgTx(testCtx, w.org, func(ctx context.Context) error {
			if _, err := testService.GrantScopeTxForTest(ctx, w.admin, w.grantTo(w.member, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL)); err != nil {
				return err
			}
			go func() {
				removed <- testService.RemoveOrgMember(testCtx, w.admin, &gen.RemoveOrgMemberRequest{OrgId: w.org, UserId: w.member})
			}()
			requireARowLockWaiter(t, "the removal should wait on the membership lock the grant holds")
			select {
			case err := <-removed:
				t.Fatalf("the removal finished while the grant held the membership: %v", err)
			default:
			}
			return nil
		}))
		require.NoError(t, <-removed)
	})
}
