//go:build !pure

package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/redisstate"
)

// useRevocationList repoints the service at a minter that keeps a revocation
// list, as a deployment with a cache does. The default test minter has none:
// every revoke succeeds and revokes nothing, so no assertion about an access
// token dying could fail against it.
func useRevocationList(t *testing.T, state redisstate.State) {
	t.Helper()
	if !minterSwapped.CompareAndSwap(false, true) {
		t.Fatal("another test already holds the shared minter: these tests mutate testService and must not run in parallel")
	}
	minter := newTestMinter(ed25519minter.Config{})
	minter.SetRevoker(redisstate.NewTokenRevoker(state))
	testService.SetJWTMinter(minter)
	t.Cleanup(func() {
		testService.SetJWTMinter(newTestMinter(ed25519minter.Config{}))
		minterSwapped.Store(false)
	})
}

// issuedAccess is every access token one login still has outstanding after a
// refresh: the one minted at sign-in and the one the refresh minted. They carry
// different session ids and one family, which is why both are asserted — a
// revocation that reaches only the current session leaves the earlier token.
type issuedAccess struct {
	beforeRefresh string
	afterRefresh  string
}

func loginAndRefresh(t *testing.T, userID string) issuedAccess {
	t.Helper()
	login := mintAdminLogin(t, userID)
	rotated, err := testService.JWTMinter().VerifyRefresh(testCtx, login.RefreshToken)
	require.NoError(t, err)
	return issuedAccess{beforeRefresh: login.AccessToken, afterRefresh: rotated.AccessToken}
}

func requireAccessLive(t *testing.T, access issuedAccess) {
	t.Helper()
	for name, token := range map[string]string{
		"minted at sign-in": access.beforeRefresh, "minted by the refresh": access.afterRefresh,
	} {
		_, err := testService.JWTMinter().VerifyAccess(token)
		require.NoError(t, err, "the access token %s must still be admitted", name)
	}
}

func requireAccessRevoked(t *testing.T, access issuedAccess) {
	t.Helper()
	for name, token := range map[string]string{
		"minted at sign-in": access.beforeRefresh, "minted by the refresh": access.afterRefresh,
	} {
		_, err := testService.JWTMinter().VerifyAccess(token)
		require.ErrorIs(t, err, auth.ErrTokenRevoked,
			"the access token %s must be refused now, not at its natural expiry", name)
	}
}

// Suspension is how a compromised account is contained. The status change
// revokes the session rows, which ends refreshing; the access tokens already
// issued are admitted from the revocation list alone, so without a marker they
// stayed usable for their whole lifetime.
func TestSuspendingAUserRevokesItsOutstandingAccessTokens(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	adminID, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	grantPlatformRole(t, adminID, "super_admin", adminID)
	target := mustUser(t, "target@example.com", "example-target")
	bystander := mustUser(t, "bystander@example.com", "example-bystander")

	targetAccess := loginAndRefresh(t, target)
	bystanderAccess := loginAndRefresh(t, bystander)
	requireAccessLive(t, targetAccess)

	require.NoError(t, testService.SuspendUser(testCtx, adminID,
		&gen.SuspendUserRequest{UserId: target, Reason: "test"}))

	requireAccessRevoked(t, targetAccess)
	requireAccessLive(t, bystanderAccess)
	require.Empty(t, adminSessions(t, target), "the durable half: no session row is left to refresh")
}

// The same for a deletion, on both scopes it runs under: the identity deleting
// itself, and a platform administrator deleting somebody else.
func TestDeletingAUserRevokesItsOutstandingAccessTokens(t *testing.T) {
	for name, accessFor := range map[string]func(target string) business.Identity{
		"by the identity itself":      func(target string) business.Identity { return business.Identity{UserID: target} },
		"by a platform administrator": func(string) business.Identity { return business.System() },
	} {
		t.Run(name, func(t *testing.T) {
			clearData(t)
			useRevocationList(t, redisstate.NewMemory())

			target := mustUser(t, "target@example.com", "example-target")
			bystander := mustUser(t, "bystander@example.com", "example-bystander")
			targetAccess := loginAndRefresh(t, target)
			bystanderAccess := loginAndRefresh(t, bystander)
			requireAccessLive(t, targetAccess)

			require.NoError(t, deleteUser(testService, accessFor(target), target))

			requireAccessRevoked(t, targetAccess)
			requireAccessLive(t, bystanderAccess)
		})
	}
}

// A deletion refused for administrative continuity changed nothing, so it must
// not have signed the administrator out either.
func TestARefusedDeletionRevokesNothing(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	owner, org := mustUserAndOrg(t, testCtx, "owner@example.com", "example-owner", "Acme")
	mustMember(t, owner, org, "member@example.com", "example-member")
	ownerAccess := loginAndRefresh(t, owner)

	err := deleteUser(testService, business.Identity{UserID: owner}, owner)
	require.ErrorIs(t, err, business.ErrIdentityAdminContinuity)

	requireAccessLive(t, ownerAccess)
	require.Len(t, adminSessions(t, owner), 1, "the refused deletion must leave the session row live")
}

// The administrator's session kill names a device family. A marker for the
// family's current session alone left the access token minted before its last
// refresh admitted.
func TestRevokingASessionRevokesEveryAccessTokenOfItsFamily(t *testing.T) {
	clearData(t)
	useRevocationList(t, redisstate.NewMemory())

	adminID, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	grantPlatformRole(t, adminID, "support", adminID)
	target := mustUser(t, "target@example.com", "example-target")
	targetAccess := loginAndRefresh(t, target)

	sessions := adminSessions(t, target)
	require.Len(t, sessions, 1)
	require.NoError(t, testService.RevokeSession(testCtx, adminID,
		&gen.RevokeSessionRequest{SessionId: sessions[0].FamilyID}))

	requireAccessRevoked(t, targetAccess)
}

// unwritableState is a revocation store that is down for writes, standing in
// for a cache outage at the moment of the suspension.
type unwritableState struct {
	redisstate.State
	attempts *int
}

func (s unwritableState) Set(context.Context, string, []byte, time.Duration) error {
	*s.attempts++
	return errors.New("revocation store unreachable")
}

// The marker is best-effort, as on every other path that writes one: the status
// change and the revoked rows are the durable authority, and a cache outage
// must not be what keeps a compromised account active.
func TestSuspensionSurvivesAnUnwritableRevocationList(t *testing.T) {
	clearData(t)
	attempts := 0
	useRevocationList(t, unwritableState{State: redisstate.NewMemory(), attempts: &attempts})

	adminID, _ := mustUserAndOrg(t, testCtx, "admin@example.com", "example-admin", "Acme")
	grantPlatformRole(t, adminID, "super_admin", adminID)
	target := mustUser(t, "target@example.com", "example-target")
	loginAndRefresh(t, target)

	require.NoError(t, testService.SuspendUser(testCtx, adminID,
		&gen.SuspendUserRequest{UserId: target, Reason: "test"}))

	require.Equal(t, "suspended", userStatus(t, target))
	require.Empty(t, adminSessions(t, target), "refreshing ends whether or not the marker was written")
	require.Positive(t, attempts, "the marker write must have been attempted")
}
