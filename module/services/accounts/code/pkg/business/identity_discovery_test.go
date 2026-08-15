package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// discoveryFakeStore is a partial fake — embeds the Store interface and
// overrides only the pre-auth resolvers, recording which values were queried.
type discoveryFakeStore struct {
	business.Store
	byDomain    map[string]*business.OrgIdentityProvider
	byHost      map[string]*business.OrgIdentityProvider
	domainCalls []string
	hostCalls   []string
}

func (f *discoveryFakeStore) ResolveOrgProviderByEmailDomain(_ context.Context, domain string) (*business.OrgIdentityProvider, error) {
	f.domainCalls = append(f.domainCalls, domain)
	return f.byDomain[domain], nil
}

func (f *discoveryFakeStore) ResolveOrgProviderByHost(_ context.Context, host string) (*business.OrgIdentityProvider, error) {
	f.hostCalls = append(f.hostCalls, host)
	return f.byHost[host], nil
}

func discoveryService(t *testing.T, store business.Store) *business.Service {
	t.Helper()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	return svc
}

func TestDiscoverProviderByEmailResolvesAllowlistedDomain(t *testing.T) {
	const orgID = "11111111-1111-1111-1111-111111111111"
	store := &discoveryFakeStore{byDomain: map[string]*business.OrgIdentityProvider{
		"acme.example": {OrgID: orgID, Kind: business.IdentityProviderKindOIDC, Status: business.IdentityProviderStatusActive},
	}}
	svc := discoveryService(t, store)

	// The domain is normalized (lowercased, trimmed) before lookup.
	got, err := svc.DiscoverProviderByEmail(context.Background(), "  Alice@ACME.example ")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{
		Available:       true,
		OrgProviderName: "oidc:" + orgID,
		Kind:            business.IdentityProviderKindOIDC,
	}, got)
	require.Equal(t, []string{"acme.example"}, store.domainCalls)
}

// TestDiscoverProviderByEmailLeaksNothingForUnknownDomain pins the
// constant-shape guarantee: an unknown domain returns the exact zero value with
// no error, indistinguishable from any other miss.
func TestDiscoverProviderByEmailLeaksNothingForUnknownDomain(t *testing.T) {
	store := &discoveryFakeStore{byDomain: map[string]*business.OrgIdentityProvider{}}
	svc := discoveryService(t, store)

	got, err := svc.DiscoverProviderByEmail(context.Background(), "nobody@unknown.example")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{}, got)
	require.False(t, got.Available)
	require.Empty(t, got.OrgProviderName)
}

func TestDiscoverProviderByEmailIgnoresNonActiveProvider(t *testing.T) {
	store := &discoveryFakeStore{byDomain: map[string]*business.OrgIdentityProvider{
		"pending.example": {OrgID: "org", Kind: business.IdentityProviderKindOIDC, Status: business.IdentityProviderStatusPending},
	}}
	svc := discoveryService(t, store)

	got, err := svc.DiscoverProviderByEmail(context.Background(), "a@pending.example")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{}, got)
}

func TestDiscoverProviderByEmailRejectsMalformedInputWithoutQuerying(t *testing.T) {
	store := &discoveryFakeStore{byDomain: map[string]*business.OrgIdentityProvider{}}
	svc := discoveryService(t, store)

	for _, email := range []string{"", "   ", "nodomain", "@nolocal.example", "a@nodot", "a@b@c.example"} {
		got, err := svc.DiscoverProviderByEmail(context.Background(), email)
		require.NoError(t, err)
		require.Equal(t, business.ProviderDiscovery{}, got, "email %q", email)
	}
	require.Empty(t, store.domainCalls, "malformed emails must never reach the store")
}

func TestDiscoverProviderByHost(t *testing.T) {
	const orgID = "22222222-2222-2222-2222-222222222222"
	store := &discoveryFakeStore{byHost: map[string]*business.OrgIdentityProvider{
		"login.acme.example": {OrgID: orgID, Kind: business.IdentityProviderKindOIDC, Status: business.IdentityProviderStatusActive},
	}}
	svc := discoveryService(t, store)

	got, err := svc.DiscoverProviderByHost(context.Background(), "Login.Acme.Example")
	require.NoError(t, err)
	require.Equal(t, "oidc:"+orgID, got.OrgProviderName)
	require.True(t, got.Available)
	require.Equal(t, []string{"login.acme.example"}, store.hostCalls)

	got, err = svc.DiscoverProviderByHost(context.Background(), "unknown.example")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{}, got)

	got, err = svc.DiscoverProviderByHost(context.Background(), "  ")
	require.NoError(t, err)
	require.Equal(t, business.ProviderDiscovery{}, got)
	require.Equal(t, []string{"login.acme.example", "unknown.example"}, store.hostCalls, "blank host must not reach the store")
}
