package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	authcore "accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// setOrgSsoJitPolicy stamps an org's provider id and JIT provisioning policy so
// a fixture login carrying that provider org routes through SsoJitIntent.
func setOrgSsoJitPolicy(t *testing.T, orgID, providerOrgID, mode string, domains []string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			UPDATE organizations
			   SET sso_organization_id       = $2,
			       sso_provision_mode        = $3,
			       sso_default_role          = 'member',
			       sso_allowed_email_domains = $4
			 WHERE id = $1`,
			orgID, providerOrgID, mode, domains)
		return err
	}))
}

// authenticateSsoFixture drives Authenticate through the fixture path with a
// provider-asserted org id, the signal that selects SsoJitIntent.
func authenticateSsoFixture(ctx context.Context, provider, subject, email, providerOrgID string) (*gen.AuthenticateResponse, error) {
	testService.SetDevelopmentTokenValidator(&requestFixtureValidator{
		token: subject,
		claims: &authcore.Claims{
			Provider:      provider,
			Subject:       subject,
			Email:         email,
			EmailVerified: true,
			ProviderOrgID: providerOrgID,
			ExpiresAt:     time.Now().Add(time.Hour),
		},
	})
	defer testService.SetDevelopmentTokenValidator(nil)
	return testService.Authenticate(ctx, &gen.AuthenticateRequest{
		Provider:      provider,
		ProviderId:    subject,
		ProviderEmail: email,
		Authentication: &gen.AuthenticateRequest_Fixture{
			Fixture: &gen.FixtureAuthentication{Token: subject},
		},
	})
}

// TestAuthenticate_SsoJitInviteOnly_AcceptsAndNotifiesInviter proves the
// invite-only SSO login fires the same business side effects as the token invite
// path — here, the inviter notification — even though it carries no token and is
// routed through SsoJitIntent rather than InviteIntent.
func TestAuthenticate_SsoJitInviteOnly_AcceptsAndNotifiesInviter(t *testing.T) {
	clearData(t)
	inviterID, orgID := mustUserAndOrg(t, testCtx, "owner@acme.test", "owner-sub", "Acme")
	setOrgSsoJitPolicy(t, orgID, "workos-acme", "invite-only", []string{"acme.test"})
	seedPendingInvitation(t, orgID, inviterID, "invitee@acme.test", "admin", time.Now().Add(24*time.Hour))

	resp, err := authenticateSsoFixture(testCtx, "workos", "invitee-sub", "invitee@acme.test", "workos-acme")
	require.NoError(t, err)
	require.NotEmpty(t, resp.User.GetUuid())

	// The invitee joined the inviting org through the consumed invitation.
	resolved, err := testService.ResolveIdentity(testCtx, &gen.ResolveIdentityRequest{
		Provider:   "workos",
		ProviderId: "invitee-sub",
	})
	require.NoError(t, err)
	require.True(t, resolved.Found)
	require.Equal(t, orgID, resolved.OrgId)

	// The inviter is notified — the side effect the token path fires and the SSO
	// path previously skipped.
	notifs, _, err := testService.ListNotifications(testCtx, inviterID, 50, "")
	require.NoError(t, err)
	var notified bool
	for _, n := range notifs {
		if n.Title == "Invitation accepted" {
			notified = true
		}
	}
	require.True(t, notified, "inviter must be notified when an invite-only SSO login accepts the invitation")
}
