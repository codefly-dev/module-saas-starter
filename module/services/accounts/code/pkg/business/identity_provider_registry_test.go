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

// builderFunc adapts a plain function to ProviderStackBuilder so a test can hook
// behaviour (e.g. an invalidation) into the middle of a build.
type builderFunc func(context.Context, *business.OrgIdentityProvider) (business.ProviderStack, error)

func (f builderFunc) Build(ctx context.Context, p *business.OrgIdentityProvider) (business.ProviderStack, error) {
	return f(ctx, p)
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

// TestRegistryInvalidateDuringBuildIsNotLost pins the invalidation race: a
// Disable that lands while a Resolve is mid-build must not be lost by the
// build's cache write. With the pre-fix "build then blindly cache" code, the
// first Resolve would cache the stale active stack and the second would serve
// it; the generation guard makes the second Resolve re-lookup and see the
// disabled provider.
func TestRegistryInvalidateDuringBuildIsNotLost(t *testing.T) {
	const orgID = "55555555-5555-5555-5555-555555555555"
	var lookups int
	lookup := func(_ context.Context, id string) (*business.OrgIdentityProvider, error) {
		lookups++
		if lookups == 1 {
			return activeProvider(id), nil
		}
		return nil, nil // provider has been disabled by the time we re-lookup
	}
	var registry *business.IdentityProviderRegistry
	builder := builderFunc(func(_ context.Context, p *business.OrgIdentityProvider) (business.ProviderStack, error) {
		// A concurrent Disable + Invalidate lands while this build is in flight.
		registry.Invalidate(p.OrgID)
		return business.ProviderStack{Name: business.OrgProviderName(p.OrgID)}, nil
	})
	registry = business.NewIdentityProviderRegistry(lookup, builder, globalStack())

	first, err := registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "oidc:"+orgID, first.Name, "the racing call still returns the stack it built")

	second, err := registry.Resolve(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, globalStackName, second.Name,
		"an invalidation during a build must not leave a stale stack cached")
	require.Equal(t, 2, lookups, "the second resolve must re-lookup, proving nothing stale was cached")
}

// TestRegistryCacheIsBounded pins LRU eviction so the cache cannot grow with the
// number of distinct orgs that ever authenticate.
func TestRegistryCacheIsBounded(t *testing.T) {
	lookups := map[string]int{}
	lookup := func(_ context.Context, id string) (*business.OrgIdentityProvider, error) {
		lookups[id]++
		return activeProvider(id), nil
	}
	registry := business.NewIdentityProviderRegistryWithCapacity(lookup, &fakeStackBuilder{}, globalStack(), 2)

	_, _ = registry.Resolve(context.Background(), "org-a")
	_, _ = registry.Resolve(context.Background(), "org-b")
	_, _ = registry.Resolve(context.Background(), "org-c") // capacity 2 → evicts LRU org-a

	// org-b is still resident: a re-resolve is a cache hit (no new lookup).
	_, _ = registry.Resolve(context.Background(), "org-b")
	require.Equal(t, 1, lookups["org-b"], "org-b must stay cached within capacity")

	// org-a was evicted: re-resolving it rebuilds.
	_, _ = registry.Resolve(context.Background(), "org-a")
	require.Equal(t, 2, lookups["org-a"], "the evicted org must be rebuilt, proving the cache is bounded")
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
