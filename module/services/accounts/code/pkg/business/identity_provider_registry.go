package business

import (
	"context"
	"errors"
	"sync"

	"accounts/pkg/auth"
)

// ProviderStack is the resolved (validator, exchanger) pair used to complete a
// sign-in. Name is the value written to user_identities.provider.
type ProviderStack struct {
	Name      string
	Validator auth.TokenValidator
	Exchanger CodeExchanger
}

// ProviderStackBuilder builds a live provider stack from a stored configuration
// row. It is implemented in the composition root rather than here: the OIDC
// adapter packages import this package, so a concrete builder that uses them
// cannot live inside it without an import cycle.
type ProviderStackBuilder interface {
	Build(ctx context.Context, provider *OrgIdentityProvider) (ProviderStack, error)
}

// ErrProviderKindUnsupported is returned by a builder for a provider kind whose
// live stack is not yet implemented (e.g. header-jwt). Configuration for such a
// provider can still be stored; only building a runtime stack is refused.
var ErrProviderKindUnsupported = errors.New("business: identity provider kind not supported")

// ActiveProviderLookup returns the org's active provider row, or (nil, nil)
// when the org has no active per-org provider and should use the global
// default.
type ActiveProviderLookup func(ctx context.Context, orgID string) (*OrgIdentityProvider, error)

// IdentityProviderRegistry resolves an organization to its provider stack,
// building each lazily and caching it until the org's configuration changes.
// Orgs without an active provider resolve to the global default stack, so a
// single deployment serves per-org IdPs and the default provider side by side.
//
// Cache invalidation is explicit: ConfigureOrgIdentityProvider,
// ActivateOrgIdentityProvider, and DisableOrgIdentityProvider call Invalidate,
// so a disable takes effect on the next sign-in without a restart while
// already-issued sessions remain valid until they expire.
type IdentityProviderRegistry struct {
	lookup  ActiveProviderLookup
	builder ProviderStackBuilder
	global  ProviderStack

	mu    sync.Mutex
	cache map[string]ProviderStack
}

// NewIdentityProviderRegistry wires the registry with the org→config lookup,
// the concrete stack builder, and the global default stack used as the
// fallback for orgs without an active provider.
func NewIdentityProviderRegistry(lookup ActiveProviderLookup, builder ProviderStackBuilder, global ProviderStack) *IdentityProviderRegistry {
	return &IdentityProviderRegistry{
		lookup:  lookup,
		builder: builder,
		global:  global,
		cache:   make(map[string]ProviderStack),
	}
}

// Resolve returns the provider stack for an org, building and caching it on
// first use. An empty org id resolves directly to the global default.
func (r *IdentityProviderRegistry) Resolve(ctx context.Context, orgID string) (ProviderStack, error) {
	if orgID == "" {
		return r.global, nil
	}

	r.mu.Lock()
	if stack, ok := r.cache[orgID]; ok {
		r.mu.Unlock()
		return stack, nil
	}
	r.mu.Unlock()

	// Build outside the lock: the lookup and build may touch the database and
	// the provider's discovery endpoint. A concurrent duplicate build for the
	// same org is harmless — the last write wins.
	provider, err := r.lookup(ctx, orgID)
	if err != nil {
		return ProviderStack{}, err
	}

	stack := r.global
	if provider != nil {
		stack, err = r.builder.Build(ctx, provider)
		if err != nil {
			return ProviderStack{}, err
		}
	}

	r.mu.Lock()
	r.cache[orgID] = stack
	r.mu.Unlock()
	return stack, nil
}

// Invalidate drops the cached stack for an org so its next Resolve rebuilds
// from current configuration.
func (r *IdentityProviderRegistry) Invalidate(orgID string) {
	r.mu.Lock()
	delete(r.cache, orgID)
	r.mu.Unlock()
}
