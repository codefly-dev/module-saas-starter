package business_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

func oidcInput(orgID, issuer string, domains ...string) business.OrgIdentityProviderInput {
	return business.OrgIdentityProviderInput{
		OrgID:               orgID,
		Kind:                business.IdentityProviderKindOIDC,
		DisplayName:         "Acme IdP",
		Issuer:              issuer,
		ClientID:            "client-" + orgID,
		ClientSecret:        "s3cret-" + orgID,
		AllowedEmailDomains: domains,
	}
}

// TestRLS_OrgIdentityProviders_CrossTenantBlocked mirrors the Phase 2B direct
// org_id tests: each org sees only its own provider row, cross-tenant reads are
// hidden even when the SQL names the other org, and an un-wrapped read returns
// nothing (RLS fail-closed).
func TestRLS_OrgIdentityProviders_CrossTenantBlocked(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-idp@rls-test.com", "alice-idp-rls", "Acme IDP A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-idp@rls-test.com", "bob-idp-rls", "Acme IDP B")

	// Two orgs with different kind/issuer configs on one deployment.
	_, err := testService.ConfigureOrgIdentityProvider(ctx, oidcInput(orgA, "https://a.example/issuer", "a.example"))
	require.NoError(t, err)
	_, err = testService.ConfigureOrgIdentityProvider(ctx, business.OrgIdentityProviderInput{
		OrgID:               orgB,
		Kind:                business.IdentityProviderKindHeaderJWT,
		DisplayName:         "Gateway",
		JWKSURL:             "https://b.example/jwks",
		AllowedEmailDomains: []string{"b.example"},
	})
	require.NoError(t, err)

	// As A: sees A's own row.
	gotA, err := testService.GetOrgIdentityProvider(ctx, orgA)
	require.NoError(t, err)
	require.NotNil(t, gotA)
	require.Equal(t, business.IdentityProviderKindOIDC, gotA.Kind)
	require.Equal(t, "https://a.example/issuer", gotA.Issuer)

	// The client secret is stored as a cipher envelope reference, never plaintext.
	require.True(t, strings.HasPrefix(gotA.ClientSecretRef, "cfs1:vault-transit:"),
		"client secret must be persisted as an envelope reference, got %q", gotA.ClientSecretRef)
	require.NotContains(t, gotA.ClientSecretRef, "s3cret-")

	// Cross-tenant probe: from A's tx, ask the Store for B's row directly.
	require.NoError(t, testStore.WithOrgTx(ctx, orgA, func(ctx context.Context) error {
		stolen, err := testStore.GetOrgIdentityProvider(ctx, orgB)
		require.NoError(t, err)
		require.Nil(t, stolen, "RLS must hide B's identity provider from A's tx")
		return nil
	}))

	// Un-wrapped: zero rows (fail-closed).
	noWrap, err := testStore.GetOrgIdentityProvider(context.Background(), orgA)
	require.NoError(t, err)
	require.Nil(t, noWrap, "un-wrapped GetOrgIdentityProvider must return nil (RLS fail-closed)")
}

// TestOrgProviderEmailDomainDiscovery covers the pre-auth discovery contract:
// only allowlisted domains on an active provider resolve, ambiguous domains
// fail closed, and unknown domains leak nothing.
func TestOrgProviderEmailDomainDiscovery(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-disc@rls-test.com", "alice-disc-rls", "Disc A")
	_, orgB := mustUserAndOrg(t, ctx, "bob-disc@rls-test.com", "bob-disc-rls", "Disc B")

	_, err := testService.ConfigureOrgIdentityProvider(ctx, oidcInput(orgA, "https://a.example/issuer", "acme.example", "shared.example"))
	require.NoError(t, err)
	_, err = testService.ConfigureOrgIdentityProvider(ctx, oidcInput(orgB, "https://b.example/issuer", "shared.example"))
	require.NoError(t, err)

	// Pending providers are not discoverable.
	pending, err := testService.DiscoverProviderByEmail(ctx, "user@acme.example")
	require.NoError(t, err)
	require.False(t, pending.Available, "a pending provider must not be discoverable")

	require.NoError(t, testService.ActivateOrgIdentityProvider(ctx, orgA))
	require.NoError(t, testService.ActivateOrgIdentityProvider(ctx, orgB))

	// Allowlisted domain on exactly one active provider resolves.
	got, err := testService.DiscoverProviderByEmail(ctx, "User@acme.example")
	require.NoError(t, err)
	require.True(t, got.Available)
	require.Equal(t, business.OrgProviderName(orgA), got.OrgProviderName)

	// A domain allowlisted by two active providers is ambiguous → fail closed.
	ambiguous, err := testService.DiscoverProviderByEmail(ctx, "user@shared.example")
	require.NoError(t, err)
	require.False(t, ambiguous.Available, "an ambiguous domain must not resolve to any org")

	// Unknown domain leaks nothing — identical shape to any other miss.
	unknown, err := testService.DiscoverProviderByEmail(ctx, "user@nowhere.example")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{}, unknown)
}

// TestOrgProviderDisableTakesEffect asserts that disabling a provider stops new
// discovery without a restart. Registry cache invalidation is unit-tested
// separately; here we cover the persisted-state half of the same guarantee.
func TestOrgProviderDisableTakesEffect(t *testing.T) {
	clearData(t)
	ctx := testCtx

	_, orgA := mustUserAndOrg(t, ctx, "alice-dis@rls-test.com", "alice-dis-rls", "Dis A")
	_, err := testService.ConfigureOrgIdentityProvider(ctx, oidcInput(orgA, "https://a.example/issuer", "disable.example"))
	require.NoError(t, err)
	require.NoError(t, testService.ActivateOrgIdentityProvider(ctx, orgA))

	got, err := testService.DiscoverProviderByEmail(ctx, "user@disable.example")
	require.NoError(t, err)
	require.True(t, got.Available)

	require.NoError(t, testService.DisableOrgIdentityProvider(ctx, orgA))

	got, err = testService.DiscoverProviderByEmail(ctx, "user@disable.example")
	require.NoError(t, err)
	require.False(t, got.Available, "a disabled provider must not resolve for new sign-ins")

	stored, err := testService.GetOrgIdentityProvider(ctx, orgA)
	require.NoError(t, err)
	require.Equal(t, business.IdentityProviderStatusDisabled, stored.Status)
}
