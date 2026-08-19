package pgauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	pgauth "accounts/pkg/auth/pg"
)

// setSsoProvisioning writes an org's JIT provisioning policy.
func setSsoProvisioning(t *testing.T, orgID uuid.UUID, mode, defaultRole string, domains []string) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(context.Background(), func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `
			UPDATE organizations
			   SET sso_provision_mode        = $2,
			       sso_default_role          = $3,
			       sso_allowed_email_domains = $4
			 WHERE id = $1`,
			orgID, mode, defaultRole, domains)
		return err
	}))
}

func ssoJitAuditCount(t *testing.T, orgID uuid.UUID) int {
	t.Helper()
	var count int
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.sso_jit_provisioned' AND org_id = $1`,
		orgID)
	return count
}

func TestResolver_SsoJit_FirstLoginProvisions_SecondIsPlainLogin(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	setSsoProvisioning(t, orgID, "jit", "member", []string{"acme.test"})

	first, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, first.UserID)
	require.Equal(t, orgID, first.OrgID, "JIT session binds to the provider's org")
	require.Equal(t, "member", first.OrgRole, "role comes from the org's default")
	require.False(t, first.Orgless(), "a JIT user never wanders orgless")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'worker@acme.test'`)
	require.Equal(t, 1, count)
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2 AND role = 'member'`,
		orgID, first.UserID)
	require.Equal(t, 1, count, "membership is created in the same transaction")
	require.Equal(t, 1, ssoJitAuditCount(t, orgID), "JIT creation emits one audit event")

	second, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)
	require.Equal(t, first.UserID, second.UserID)
	require.Equal(t, orgID, second.OrgID)
	require.Equal(t, "member", second.OrgRole)

	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'worker@acme.test'`)
	require.Equal(t, 1, count, "second login provisions no duplicate user")
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID)
	require.Equal(t, 1, count, "second login provisions no duplicate membership")
	require.Equal(t, 1, ssoJitAuditCount(t, orgID), "a plain login emits no new JIT audit event")
}

func TestResolver_SsoJit_EmailDomainNotAllowed_RejectedAndProvisionsNothing(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	setSsoProvisioning(t, orgID, "jit", "member", []string{"acme.test"})

	_, err := r.Resolve(ctx, claims("stranger@evil.test", "sso-stranger"), auth.SsoJitIntent{OrgID: orgID})
	require.ErrorIs(t, err, auth.ErrSsoEmailDomainNotAllowed)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'stranger@evil.test'`)
	require.Equal(t, 0, count, "a non-matching email provisions no user")
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID)
	require.Equal(t, 0, count, "a non-matching email creates no membership")
	require.Equal(t, 0, ssoJitAuditCount(t, orgID))
}

func TestResolver_SsoJit_EmptyAllowlist_ReportsMisconfiguration(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	// jit enabled but no trusted domains named — a misconfiguration, distinct
	// from a specific email being outside a configured allowlist.
	setSsoProvisioning(t, orgID, "jit", "member", []string{})

	_, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.ErrorIs(t, err, auth.ErrSsoProvisioningMisconfigured)
	require.NotErrorIs(t, err, auth.ErrSsoEmailDomainNotAllowed,
		"an unconfigured allowlist is a distinct org-level error, not a per-user domain rejection")

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM organization_members WHERE org_id = $1`, orgID)
	require.Equal(t, 0, count, "a misconfigured org provisions nothing")
}

func TestResolver_SsoJit_ReprovisionsIdentityRemovedFromOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	setSsoProvisioning(t, orgID, "jit", "member", []string{"acme.test"})

	first, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)

	// Locally remove the member while the identity stays valid at the IdP.
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		_, err := tx.Exec(ctx, `DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`,
			orgID, first.UserID)
		return err
	}))

	// The IdP remains the source of truth: the next SSO login re-provisions the
	// membership rather than treating the still-valid assertion as orgless.
	second, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)
	require.Equal(t, first.UserID, second.UserID, "the same identity, no duplicate user")
	require.Equal(t, orgID, second.OrgID)
	require.Equal(t, "member", second.OrgRole)

	var count int
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, first.UserID)
	require.Equal(t, 1, count, "membership is re-provisioned")
}

func TestResolver_SsoJit_InviteOnly_RequiresPendingInvitation(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID, "Acme", "workos-acme")
	addMember(t, orgID, ownerID, "owner", time.Now())
	setSsoProvisioning(t, orgID, "invite-only", "member", []string{"acme.test"})

	// First-seen identity without an invitation is rejected, nothing written.
	_, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.ErrorIs(t, err, auth.ErrSignupNotAllowed)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'worker@acme.test'`)
	require.Equal(t, 0, count)

	// With a pending invitation, the identity binds to the invitation's org and
	// role, and the invitation is consumed.
	seedInvitation(t, orgID, ownerID, "worker@acme.test", "admin", "pending", time.Now().Add(24*time.Hour))

	id, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)
	require.Equal(t, orgID, id.OrgID)
	require.Equal(t, "admin", id.OrgRole, "role comes from the invitation, not the default")

	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2 AND role = 'admin'`,
		orgID, id.UserID)
	require.Equal(t, 1, count)

	var status string
	scanControlPlane(t, &status,
		`SELECT status FROM invitations WHERE org_id = $1 AND email = 'worker@acme.test'`, orgID)
	require.Equal(t, "accepted", status, "the invitation is consumed")
}

func TestResolver_SsoJit_Disabled_RejectsFirstSeen_ExistingMemberLogsIn(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	setSsoProvisioning(t, orgID, "disabled", "member", []string{"acme.test"})

	// First-seen identity is rejected.
	_, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: orgID})
	require.ErrorIs(t, err, auth.ErrSsoProvisioningDisabled)

	var count int
	scanControlPlane(t, &count, `SELECT COUNT(*) FROM users WHERE primary_email = 'worker@acme.test'`)
	require.Equal(t, 0, count, "disabled mode provisions no first-seen identity")

	// An existing member logs in fine regardless of the disabled mode.
	member, err := r.Resolve(ctx, claims("existing@acme.test", "sso-existing"), auth.SignupIntent{})
	require.NoError(t, err)
	addMember(t, orgID, member.UserID, "member", time.Now())

	loggedIn, err := r.Resolve(ctx, claims("existing@acme.test", "sso-existing"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err)
	require.Equal(t, orgID, loggedIn.OrgID)
	require.Equal(t, "member", loggedIn.OrgRole)
}

func TestResolver_SsoJit_ExistingMemberShortCircuits_NotReGatedByDomain(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	orgID := seedOrg(t, seedUser(t), "Acme", "workos-acme")
	// Allowlist deliberately excludes the member's email domain.
	setSsoProvisioning(t, orgID, "jit", "member", []string{"corp.test"})

	member, err := r.Resolve(ctx, claims("legacy@acme.test", "sso-legacy"), auth.SignupIntent{})
	require.NoError(t, err)
	addMember(t, orgID, member.UserID, "admin", time.Now())

	loggedIn, err := r.Resolve(ctx, claims("legacy@acme.test", "sso-legacy"), auth.SsoJitIntent{OrgID: orgID})
	require.NoError(t, err, "an existing member is not re-gated by the JIT domain allowlist")
	require.Equal(t, orgID, loggedIn.OrgID)
	require.Equal(t, "admin", loggedIn.OrgRole, "the member's own role is preserved")
	require.Equal(t, 0, ssoJitAuditCount(t, orgID), "a plain login is not a JIT creation")
}

func TestResolver_SsoJit_NeverCreatesOrgOrTouchesAnotherOrg(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	target := seedOrg(t, seedUser(t), "Target", "workos-target")
	setSsoProvisioning(t, target, "jit", "member", []string{"acme.test"})
	other := seedOrg(t, seedUser(t), "Other", "workos-other")
	setSsoProvisioning(t, other, "jit", "member", []string{"acme.test"})

	var orgsBefore int
	scanControlPlane(t, &orgsBefore, `SELECT COUNT(*) FROM organizations`)

	id, err := r.Resolve(ctx, claims("worker@acme.test", "sso-worker"), auth.SsoJitIntent{OrgID: target})
	require.NoError(t, err)
	require.Equal(t, target, id.OrgID)

	var orgsAfter int
	scanControlPlane(t, &orgsAfter, `SELECT COUNT(*) FROM organizations`)
	require.Equal(t, orgsBefore, orgsAfter, "JIT never creates an organization")

	var count int
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE user_id = $1`, id.UserID)
	require.Equal(t, 1, count, "membership is created for exactly one org")
	scanControlPlane(t, &count,
		`SELECT COUNT(*) FROM organization_members WHERE org_id = $1 AND user_id = $2`, other, id.UserID)
	require.Equal(t, 0, count, "the other org's membership is never touched")
}

func TestResolver_ResolveOrgProvisioning(t *testing.T) {
	resetAuthTables(t)
	ctx := context.Background()
	r := pgauth.NewResolver(testStore)

	withPolicy := seedOrg(t, seedUser(t), "WithPolicy", "workos-with")
	setSsoProvisioning(t, withPolicy, "jit", "member", []string{"acme.test"})
	noPolicy := seedOrg(t, seedUser(t), "NoPolicy", "workos-without")

	orgID, hasPolicy, err := r.ResolveOrgProvisioning(ctx, "workos-with")
	require.NoError(t, err)
	require.True(t, hasPolicy)
	require.Equal(t, withPolicy, orgID)

	orgID, hasPolicy, err = r.ResolveOrgProvisioning(ctx, "workos-without")
	require.NoError(t, err)
	require.False(t, hasPolicy, "an org without a policy keeps today's intent selection")
	require.Equal(t, noPolicy, orgID)

	_, hasPolicy, err = r.ResolveOrgProvisioning(ctx, "workos-unknown")
	require.NoError(t, err)
	require.False(t, hasPolicy)

	_, hasPolicy, err = r.ResolveOrgProvisioning(ctx, "")
	require.NoError(t, err)
	require.False(t, hasPolicy, "a global-provider login asserts no org")
}
