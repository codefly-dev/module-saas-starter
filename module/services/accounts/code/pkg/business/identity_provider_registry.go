package business

import (
	"container/list"
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

// defaultProviderCacheCapacity bounds the resolved-stack cache so memory does
// not grow with the number of distinct orgs that ever authenticate. Stacks are
// cheap to rebuild (a lookup plus, for a cache miss, OIDC discovery), so a
// generous cap with LRU eviction keeps hot tenants resident without unbounded
// growth.
const defaultProviderCacheCapacity = 2048

type cachedStack struct {
	orgID string
	stack ProviderStack
}

// IdentityProviderRegistry resolves an organization to its provider stack,
// building each lazily and caching it until the org's configuration changes.
// Orgs without an active provider resolve to the global default stack, so a
// single deployment serves per-org IdPs and the default provider side by side.
//
// Cache invalidation is explicit: ConfigureOrgIdentityProvider,
// ActivateOrgIdentityProvider, and DisableOrgIdentityProvider call Invalidate,
// so a disable takes effect on the next sign-in without a restart while
// already-issued sessions remain valid until they expire.
//
// The cache is a bounded LRU. Each cache miss builds outside the lock (lookup
// and build may touch the database and the provider's discovery endpoint) and
// records the generation observed before the build; the result is only stored
// if no Invalidate has run since, so an invalidation that races an in-flight
// build is never lost and cannot leave a stale stack resident.
type IdentityProviderRegistry struct {
	lookup   ActiveProviderLookup
	builder  ProviderStackBuilder
	global   ProviderStack
	capacity int

	mu         sync.Mutex
	entries    map[string]*list.Element // orgID → *cachedStack element
	lru        *list.List               // front = most recently used
	generation uint64
}

// NewIdentityProviderRegistry wires the registry with the org→config lookup,
// the concrete stack builder, and the global default stack used as the
// fallback for orgs without an active provider.
func NewIdentityProviderRegistry(lookup ActiveProviderLookup, builder ProviderStackBuilder, global ProviderStack) *IdentityProviderRegistry {
	return NewIdentityProviderRegistryWithCapacity(lookup, builder, global, defaultProviderCacheCapacity)
}

// NewIdentityProviderRegistryWithCapacity is NewIdentityProviderRegistry with an
// explicit cache capacity. A non-positive capacity falls back to the default.
func NewIdentityProviderRegistryWithCapacity(lookup ActiveProviderLookup, builder ProviderStackBuilder, global ProviderStack, capacity int) *IdentityProviderRegistry {
	if capacity <= 0 {
		capacity = defaultProviderCacheCapacity
	}
	return &IdentityProviderRegistry{
		lookup:   lookup,
		builder:  builder,
		global:   global,
		capacity: capacity,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

// Resolve returns the provider stack for an org, building and caching it on
// first use. An empty org id resolves directly to the global default.
func (r *IdentityProviderRegistry) Resolve(ctx context.Context, orgID string) (ProviderStack, error) {
	if orgID == "" {
		return r.global, nil
	}

	r.mu.Lock()
	if el, ok := r.entries[orgID]; ok {
		r.lru.MoveToFront(el)
		stack := el.Value.(*cachedStack).stack
		r.mu.Unlock()
		return stack, nil
	}
	gen := r.generation
	r.mu.Unlock()

	// Build outside the lock: the lookup and build may touch the database and
	// the provider's discovery endpoint.
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
	// Only cache if no Invalidate ran while we were building. Otherwise this
	// result may reflect configuration that a concurrent config change has
	// already superseded, and caching it would silently lose that invalidation.
	if r.generation == gen {
		r.store(orgID, stack)
	}
	r.mu.Unlock()
	return stack, nil
}

// store inserts or refreshes an entry and evicts the least-recently-used entry
// when over capacity. Caller holds r.mu.
func (r *IdentityProviderRegistry) store(orgID string, stack ProviderStack) {
	if el, ok := r.entries[orgID]; ok {
		el.Value.(*cachedStack).stack = stack
		r.lru.MoveToFront(el)
		return
	}
	r.entries[orgID] = r.lru.PushFront(&cachedStack{orgID: orgID, stack: stack})
	for r.lru.Len() > r.capacity {
		oldest := r.lru.Back()
		if oldest == nil {
			break
		}
		r.lru.Remove(oldest)
		delete(r.entries, oldest.Value.(*cachedStack).orgID)
	}
}

// Invalidate drops the cached stack for an org so its next Resolve rebuilds
// from current configuration, and advances the generation so any build already
// in flight does not re-cache a now-stale stack.
func (r *IdentityProviderRegistry) Invalidate(orgID string) {
	r.mu.Lock()
	if el, ok := r.entries[orgID]; ok {
		r.lru.Remove(el)
		delete(r.entries, orgID)
	}
	r.generation++
	r.mu.Unlock()
}
