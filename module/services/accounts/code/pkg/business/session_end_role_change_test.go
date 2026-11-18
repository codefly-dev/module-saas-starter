//go:build !pure

package business_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A role change inside an organisation leaves the session standing: the
// database revokes its row, so nothing of it is refreshed, and the bearer
// already issued is still a session of this host until it lapses or is ended.
// The shared host-interface suite takes an administrator's role away and gives
// it back on one session and asks that session what it may do in between.
// Reading the revoked row as "no open session" answered it Unauthenticated.
// What ends a session is its own End, a removal from the organisation, or its
// lifetime.
func TestARoleChangeLeavesTheSessionOpenUntilItIsEndedOrTheMemberRemoved(t *testing.T) {
	clearData(t)
	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	member := mustMember(t, owner, org, "member@example.com", "example-member")
	setRole := func(role gen.OrgRole) {
		t.Helper()
		require.NoError(t, testService.AddOrgMember(testCtx, owner, &gen.AddOrgMemberRequest{OrgId: org, UserId: member, Role: role}))
	}
	signIn := func() string {
		t.Helper()
		pair, err := testService.JWTMinter().Mint(testCtx, &auth.Identity{
			UserID: uuid.MustParse(member), OrgID: uuid.MustParse(org), SessionID: business.NewID(),
		})
		require.NoError(t, err)
		identity, err := testService.JWTMinter().VerifyAccess(pair.AccessToken)
		require.NoError(t, err)
		return identity.SessionID.String()
	}

	session := signIn()
	_, err := testService.SessionEndsAt(testCtx, session)
	require.NoError(t, err, "a session just opened is open")
	require.NoError(t, testService.CheckCapabilitySession(testCtx, session))
	require.NoError(t, testService.CheckCapabilitySession(testCtx, uuid.NewString()), "noninteractive roots do not name session rows")

	setRole(gen.OrgRole_ORG_ROLE_ADMIN)
	_, err = testService.SessionEndsAt(testCtx, session)
	require.NoError(t, err, "a role given leaves the session standing")
	setRole(gen.OrgRole_ORG_ROLE_MEMBER)
	_, err = testService.SessionEndsAt(testCtx, session)
	require.NoError(t, err, "and a role taken away does too")
	require.NoError(t, testService.CheckCapabilitySession(testCtx, session))

	// It is still the session's to end, once.
	require.NoError(t, testService.EndSession(testCtx, member, org, session))
	_, err = testService.SessionEndsAt(testCtx, session)
	require.ErrorIs(t, err, business.ErrNoOpenSession, "an ended session is not open")
	require.ErrorIs(t, testService.CheckCapabilitySession(testCtx, session), business.ErrNoOpenSession)
	require.ErrorIs(t, testService.EndSession(testCtx, member, org, session), business.ErrNoOpenSession, "and ending is never undone")

	// A removal is not a role change: the leaver's session is over.
	leaving := signIn()
	require.NoError(t, testService.RemoveOrgMember(testCtx, owner, &gen.RemoveOrgMemberRequest{OrgId: org, UserId: member}))
	_, err = testService.SessionEndsAt(testCtx, leaving)
	require.ErrorIs(t, err, business.ErrNoOpenSession, "a removed member's session is not open")
}
