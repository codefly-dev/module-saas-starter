//go:build pure

package business

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A MODULE IDENTITY MINTED FOR ONE TENANT, read for an operator (issue #1121).
//
// The capability itself is the headless operation mint naming an organization,
// and that it mints per tenant — with that tenant's own installation narrowing
// what it carries — is proved against the mint in
// TestSharedInstallationMintSelectsOnlyConsentingOrganization. What is tested
// here is the READ an operator drives the playground from: it must answer from
// the same declared facts the mint decides on, and it must issue nothing.
//
// The refusals are four separate answers because they are four different things
// an operator does about them: declare the principal, widen the envelope's
// tenancy, install the module in that organization, or consent to the binding's
// headless scopes there. One collapsed refusal would answer none of them.

// perTenantIdentityFixture is the shared-principal deployment: one module
// unpinned and cross-tenant, installed in two organizations, whose binding
// declares headless scopes — and whose second organization consented to fewer
// of them. A third organization has no installation at all.
func perTenantIdentityFixture(t *testing.T) (*Service, string, string, string) {
	t.Helper()
	s, live, caller := dynamicInstallationFixture()
	grant := s.ModulePrincipals()[caller.PrincipalID]
	binding := grant.OperationAudiences["operate"]
	binding.HeadlessScopes = cloneOperationScopes(binding.InvokeScopes)
	grant.OperationAudiences["operate"] = binding
	live.rows["org-a/principal"].Scope = FullInstallationScope(grant)
	// The second organization's installation consents to a narrower binding,
	// which is what makes the bound per tenant rather than per deployment.
	narrowed := live.rows["org-b/principal"].Scope.OperationAudiences["operate"]
	narrowed.HeadlessScopes = cloneOperationScopes(narrowed.InvokeScopes)
	live.rows["org-b/principal"].Scope.OperationAudiences["operate"] = narrowed

	id := ModulePrincipalID(grant.Prefix)
	first, second, third := uuid.NewString(), uuid.NewString(), uuid.NewString()
	live.rows[first+"/"+id] = live.rows["org-a/principal"]
	live.rows[second+"/"+id] = live.rows["org-b/principal"]
	// Unpinned and cross-tenant: the shape a module that acts for many tenants
	// is declared in, and the only one that may name an organization per mint.
	grant.Tenant = ""
	s.SetModulePrincipals(ModulePrincipalRegistry{id: grant})
	// The resolution also reads the trail of capabilities actually issued, in
	// the organization's own transaction. The store is swapped rather than the
	// read made optional: a read that swallowed its own failure would report
	// "no capability was ever issued" over a database that could not answer.
	s.store = perTenantIdentityStore{Store: s.store}
	return s, first, second, third
}

// perTenantIdentityStore answers the organization-scoped audit read with an
// empty trail. Empty rather than seeded: what the trail CONTAINS is the audit
// log's own contract, tested next door; what matters here is that the
// resolution asks for it inside the organization's transaction and does not
// invent a tenant for the question.
type perTenantIdentityStore struct{ Store }

func (perTenantIdentityStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (perTenantIdentityStore) QueryAuditLog(context.Context, AuditQuery) ([]AuditEntry, string, int32, error) {
	return nil, "", 0, nil
}

func TestResolveModuleTenantIdentityAnswersWhatOneTenantsMintWouldAssert(t *testing.T) {
	s, first, second, third := perTenantIdentityFixture(t)
	ctx := context.Background()

	full, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", first)
	require.NoError(t, err)
	require.Empty(t, full.Refusal)
	require.NotNil(t, full.Identity)
	require.Equal(t, ModulePrincipalID("example"), full.Identity.PrincipalID)
	require.Equal(t, first, full.Identity.Tenant, "the capability acts FOR the organization named")
	require.Equal(t, "example.api", full.Identity.Audience)
	require.Equal(t, ModuleOperationContextTTL, full.Identity.Lifetime)
	require.NotZero(t, full.Identity.Revision)
	require.True(t, full.Identity.Shared, "an unpinned cross-tenant principal may act for more than one organization")
	require.Equal(t, []string{
		"records:read:one", "records:read:two", "records:write:one", "records:write:two",
	}, full.Identity.Scopes)

	// THE SAME MODULE, THE SAME BINDING, A DIFFERENT TENANT: fewer scopes,
	// because that organization's installation consented to fewer. This is the
	// property a single deployment-wide identity cannot have.
	narrowed, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", second)
	require.NoError(t, err)
	require.NotNil(t, narrowed.Identity)
	require.Equal(t, second, narrowed.Identity.Tenant)
	require.Equal(t, []string{"records:read:one"}, narrowed.Identity.Scopes)
	require.NotEqual(t, full.Identity.Scopes, narrowed.Identity.Scopes,
		"the two tenants' capabilities carried the same authority, so nothing is bounded per tenant")

	// An organization with no live installation: the module may act for a
	// tenant only where that tenant installed it.
	absent, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", third)
	require.NoError(t, err)
	require.Nil(t, absent.Identity)
	require.Equal(t, ModuleTenantIdentityNotInstalled, absent.Refusal)

	// A binding that is not declared, and one that declares no headless
	// scopes: neither is mintable with no person present.
	undeclared, err := s.ResolveModuleTenantIdentity(ctx, "example", "absent", first)
	require.NoError(t, err)
	require.Equal(t, ModuleTenantIdentityBindingRefused, undeclared.Refusal)

	// A prefix no module principal is declared under.
	unknown, err := s.ResolveModuleTenantIdentity(ctx, "nobody", "operate", first)
	require.NoError(t, err)
	require.Equal(t, ModuleTenantIdentityUndeclared, unknown.Refusal)

	// And the arguments it cannot answer without.
	_, err = s.ResolveModuleTenantIdentity(ctx, "", "operate", first)
	require.Error(t, err)
	_, err = s.ResolveModuleTenantIdentity(ctx, "example", "operate", "")
	require.Error(t, err)
}

// A PINNED PRINCIPAL MAY ACT FOR ITS OWN TENANT AND NO OTHER.
//
// The pin is the bound, so the refusal for another organization is the
// envelope's decision and not a missing feature: a module that should act for
// many tenants is declared unpinned and cross-tenant, which is the fixture
// above. Reported as its own refusal because what an operator does about it —
// change the envelope — is not what they do about a missing installation.
func TestResolveModuleTenantIdentityRefusesATenantThePinForbids(t *testing.T) {
	s, first, second, _ := perTenantIdentityFixture(t)
	grant := s.ModulePrincipals()[ModulePrincipalID("example")]
	grant.Tenant = first
	s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID("example"): grant})
	ctx := context.Background()

	own, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", first)
	require.NoError(t, err)
	require.NotNil(t, own.Identity)
	require.False(t, own.Identity.Shared, "a pinned principal acts for one organization, and the read says so")

	other, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", second)
	require.NoError(t, err)
	require.Nil(t, other.Identity)
	require.Equal(t, ModuleTenantIdentityTenantRefused, other.Refusal)

	// Unpinned but NOT cross-tenant is refused the same way: naming an
	// organization is not authority over it, and a shared mint needs the
	// declaration that says so.
	grant.Tenant, grant.CrossTenant = "", false
	s.SetModulePrincipals(ModulePrincipalRegistry{ModulePrincipalID("example"): grant})
	neither, err := s.ResolveModuleTenantIdentity(ctx, "example", "operate", first)
	require.NoError(t, err)
	require.Equal(t, ModuleTenantIdentityTenantRefused, neither.Refusal)
}
