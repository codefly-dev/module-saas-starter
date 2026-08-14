package business_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

type fakeStackBuilder struct {
	builds int64
	err    error
}

func (b *fakeStackBuilder) Build(_ context.Context, p *business.OrgIdentityProvider) (business.ProviderStack, error) {
	atomic.AddInt64(&b.builds, 1)
	if b.err != nil {
		return business.ProviderStack{}, b.err
	}
	return business.ProviderStack{Name: business.OrgProviderName(p.OrgID)}, nil
}

func activeProvider(orgID string) *business.OrgIdentityProvider {
	return &business.OrgIdentityProvider{
		OrgID:  orgID,
		Kind:   business.IdentityProviderKindOIDC,
		Status: business.IdentityProviderStatusActive,
	}
}

func lookupFrom(rows map[string]*business.OrgIdentityProvider) business.ActiveProviderLookup {
	return func(_ context.Context, orgID string) (*business.OrgIdentityProvider, error) {
		return rows[orgID], nil
	}
}

const globalStackName = "workos"

func globalStack() business.ProviderStack {
	return business.ProviderStack{Name: globalStackName}
}

func TestRegistryBuildsAndCachesPerOrgStack(t *testing.T) {
	const orgID = "11111111-1111-1111-1111-111111111111"
	builder := &fakeStackBuilder{}
	registry := business.NewIdentityProviderRegistry(
		lookupFrom(map[string]*business.OrgIdentityProvider{orgID: activeProvider(orgID)}),
		builder, globalStack())

	stack, err := registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "oidc:"+orgID, stack.Name)
	require.Equal(t, int64(1), atomic.LoadInt64(&builder.builds))

	// Second resolve is served from cache — no rebuild.
	stack, err = registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "oidc:"+orgID, stack.Name)
	require.Equal(t, int64(1), atomic.LoadInt64(&builder.builds), "cached resolve must not rebuild")
}

func TestRegistryInvalidateForcesRebuild(t *testing.T) {
	const orgID = "22222222-2222-2222-2222-222222222222"
	builder := &fakeStackBuilder{}
	registry := business.NewIdentityProviderRegistry(
		lookupFrom(map[string]*business.OrgIdentityProvider{orgID: activeProvider(orgID)}),
		builder, globalStack())

	_, err := registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	registry.Invalidate(orgID)
	_, err = registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, int64(2), atomic.LoadInt64(&builder.builds), "invalidation must force a rebuild on next resolve")
}

func TestRegistryFallsBackToGlobalWithoutActiveProvider(t *testing.T) {
	const orgID = "33333333-3333-3333-3333-333333333333"
	builder := &fakeStackBuilder{}
	registry := business.NewIdentityProviderRegistry(
		lookupFrom(map[string]*business.OrgIdentityProvider{}), builder, globalStack())

	stack, err := registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, globalStackName, stack.Name, "orgs without a provider must use the global default")
	require.Equal(t, int64(0), atomic.LoadInt64(&builder.builds), "the global fallback must not invoke the builder")

	// Empty org id resolves directly to the global default.
	stack, err = registry.Resolve(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, globalStackName, stack.Name)
}

// TestRegistryPerOrgProviderNamesNeverCollide is the acceptance-criterion check:
// identities from different tenants' IdPs must never share a provider name,
// even when their upstream subjects are identical.
func TestRegistryPerOrgProviderNamesNeverCollide(t *testing.T) {
	const orgA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const orgB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	builder := &fakeStackBuilder{}
	registry := business.NewIdentityProviderRegistry(
		lookupFrom(map[string]*business.OrgIdentityProvider{
			orgA: activeProvider(orgA),
			orgB: activeProvider(orgB),
		}), builder, globalStack())

	stackA, err := registry.Resolve(context.Background(), orgA)
	require.NoError(t, err)
	stackB, err := registry.Resolve(context.Background(), orgB)
	require.NoError(t, err)

	require.NotEqual(t, stackA.Name, stackB.Name)
	require.Equal(t, "oidc:"+orgA, stackA.Name)
	require.Equal(t, "oidc:"+orgB, stackB.Name)
}

func TestRegistryPropagatesBuilderError(t *testing.T) {
	const orgID = "44444444-4444-4444-4444-444444444444"
	sentinel := errors.New("build failed")
	registry := business.NewIdentityProviderRegistry(
		lookupFrom(map[string]*business.OrgIdentityProvider{orgID: activeProvider(orgID)}),
		&fakeStackBuilder{err: sentinel}, globalStack())

	_, err := registry.Resolve(context.Background(), orgID)
	require.ErrorIs(t, err, sentinel)
}
