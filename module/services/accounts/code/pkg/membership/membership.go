// Package membership caches org membership — "user X holds role Z in org Y" —
// on a codefly.dev/cache stack. Every authorized endpoint asks it, so it is the
// one accounts lookup worth caching. The store is the source of truth; this
// package only keeps copies of what the store answers.
//
// The stack is, top to bottom:
//
//   - an in-process Memory tier, fresh for a few seconds;
//   - a shared layer (the Redis driver in a running service), fresh for 30s;
//   - the origin: the store's indexed membership read.
//
// What that buys over a plain Redis GET: a miss reaches the store once across
// every replica (singleflight in a process, a fill lease on the shared layer
// across processes); an invalidation on any replica evicts the other replicas'
// in-process copies (the shared layer is a Notifier); an unreachable Redis falls
// through to the store behind a breaker; and "not a member" is cached as a
// negative entry.
//
// # How stale a copy can be after an invalidation
//
// Up to memoryTTL (5s) on another replica, not zero. The stack can store a
// generation it read from the shared layer into the in-process tier AFTER the
// change notice for that key arrived: the replica then serves its pre-change
// copy until the in-process entry expires. That fill path is being rewritten in
// codefly-dev/interface-cache#7. Until it lands, memoryTTL is the bound, which
// is why it stays short.
//
// # Store-only mode
//
// Connect retries a shared layer it cannot reach for a bounded time, then
// returns a Cache that reads every membership from the store: never stale, so
// no removed member keeps access, and a Redis outage at boot does not take
// authorization down. StoreOnly reports it; the service makes it loud.
//
// # Partition
//
// Every read is partitioned by tenant — the org — and nothing finer. That is
// enough because the value does not vary by viewer: the store's read binds the
// database identity to exactly (org, user) and selects that one row, and
// RequireVerifiedDatabaseScope admits a caller to the key (org, user) only when
// its verified identity IS (org, user). So every caller that can share a load —
// including the one whose context the shared load runs under — reads the same
// row with the same database identity. Sessions, scopes, Work Context views and
// authorization revisions change nothing about that row.
//
// If a value that depends on who is asking ever enters this cache (a view
// filtered by scope, a row RLS answers differently per session), the partition
// must carry the viewer: the SDK's workcontext ByViewer (sdk-go#42), not
// ByAuthorizationView, which omits the subject and would let two users with the
// same scopes share one another's entries.
//
// The key names the org as well as the user because Invalidate replaces a key's
// generation across every partition: the key alone must identify the fact. Both
// ids are canonicalized first: the scope check and the store accept every
// spelling uuid.Parse does, so a raw id would give one membership one key per
// spelling, and an invalidation under one spelling would miss the others.
package membership

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cache "github.com/codefly-dev/interface-cache/go/cache"
	"github.com/google/uuid"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	// memoryTTL bounds the in-process copy, and with it how long another
	// replica can serve a copy after an invalidation (see the package doc).
	memoryTTL = 5 * time.Second
	// sharedTTL is the ceiling on how long a membership change can go unseen
	// when no invalidation runs (a write that bypasses the business layer). It
	// is the 30s the previous Redis cache used; explicit invalidation on every
	// membership mutation brings it down to the in-process bound.
	sharedTTL = 30 * time.Second
	// memoryEntries caps the in-process tier. Each cached membership costs two
	// entries (its generation and its copy).
	memoryEntries = 100_000

	keyPrefix = "orgmember:"
)

// Store is the origin: the store's indexed, RLS-scoped membership read.
// A nil membership with a nil error is "not a member".
type Store interface {
	GetOrgMembership(ctx context.Context, orgID, userID string) (*gen.OrgMembership, error)
}

// Cache is org membership behind a cache stack, or straight from the store in
// store-only mode. Safe for concurrent use.
type Cache struct {
	stack *cache.Stack // nil in store-only mode
	store Store
	// storeOnly is why there is no stack, nil when there is one.
	storeOnly error
	evict     func(ctx context.Context, orgID, userID string) error
}

// Option configures a Cache.
type Option func(*Cache)

// WithEviction runs evict with the canonical ids on every Invalidate, in
// either mode. It is how the service evicts copies this stack does not own,
// such as the previous release's keys during a rollout.
func WithEviction(evict func(ctx context.Context, orgID, userID string) error) Option {
	return func(c *Cache) { c.evict = evict }
}

// New builds the stack over shared, with store as its origin. shared is the
// layer every replica reaches: the Redis driver in a running service, a
// cache.Memory (Share()d between instances) in tests. New subscribes to
// shared's change notices, so it fails when shared cannot be reached; Connect
// is New with a bounded retry and a store-only fallback.
func New(ctx context.Context, shared cache.Layer, store Store, opts ...Option) (*Cache, error) {
	if shared == nil || store == nil {
		return nil, errors.New("membership: New needs a shared layer and a store")
	}
	stack, err := cache.New(ctx,
		cache.WithTier(cache.NewMemory(cache.MaxEntries(memoryEntries)), memoryTTL),
		cache.WithTier(shared, sharedTTL),
		cache.WithOrigin(origin(store)),
		// "Not a member" is cached as long as a role is: every membership
		// mutation invalidates the key, negative or not.
		cache.WithNegativeTTL(sharedTTL),
	)
	if err != nil {
		return nil, fmt.Errorf("membership: build cache stack: %w", err)
	}
	c := &Cache{stack: stack, store: store}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Retry bounds how long Connect waits for the shared layer.
type Retry struct {
	// Attempts is how many times the stack is built, at least one.
	Attempts int
	// Wait is the pause after the first failed attempt, doubled after each.
	Wait time.Duration
	// AttemptTimeout bounds one attempt, so a server that does not answer
	// costs this, not the client's dial timeout.
	AttemptTimeout time.Duration
}

// BootRetry is what the service boots with: about 12s at worst (five
// 2s-bounded attempts, 3.75s of waits) before it settles on store-only.
var BootRetry = Retry{Attempts: 5, Wait: 250 * time.Millisecond, AttemptTimeout: 2 * time.Second}

// Connect is New, retried while the shared layer cannot be reached. When every
// attempt fails it returns a store-only Cache, whose StoreOnly reports the last
// cause, rather than an error: a cache that cannot hear invalidations must not
// serve copies, and reading the store instead is never stale. It errors only on
// invalid arguments or when ctx ends.
func Connect(ctx context.Context, shared cache.Layer, store Store, retry Retry, opts ...Option) (*Cache, error) {
	if shared == nil || store == nil {
		return nil, errors.New("membership: Connect needs a shared layer and a store")
	}
	wait := retry.Wait
	var cause error
	for attempt := range max(retry.Attempts, 1) {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		attemptCtx, cancel := context.WithTimeout(ctx, retry.AttemptTimeout)
		c, err := New(attemptCtx, shared, store, opts...)
		cancel()
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cause = fmt.Errorf("attempt %d of %d: %w", attempt+1, max(retry.Attempts, 1), err)
	}
	c := &Cache{store: store, storeOnly: cause}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// StoreOnly is why every read goes to the store, nil when the stack is up.
func (c *Cache) StoreOnly() error { return c.storeOnly }

// Role returns userID's role in orgID, or "" when the user is verifiably not a
// member. The caller's verified identity must be exactly (orgID, userID): the
// cache stores data, never decisions, so the same scope check that guards the
// store read runs before any layer is consulted.
func (c *Cache) Role(ctx context.Context, orgID, userID string) (string, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, orgID, userID); err != nil {
		return "", err
	}
	orgID, userID, err := canonical(orgID, userID)
	if err != nil {
		return "", err
	}
	if c.stack == nil {
		return load(ctx, c.store, orgID, userID)
	}
	value, err := c.stack.Get(ctx, partition(orgID), key(orgID, userID))
	if errors.Is(err, cache.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// Invalidate drops every replica's copy of (orgID, userID), in every layer. The
// business layer calls it after each committed membership mutation. It runs in
// the mutating actor's context, which is not the member's, so it takes no
// identity check: it reads nothing, and only replaces the key's generation.
// Every step is attempted and every failure reported.
func (c *Cache) Invalidate(ctx context.Context, orgID, userID string) error {
	orgID, userID, err := canonical(orgID, userID)
	if err != nil {
		return fmt.Errorf("membership: Invalidate: %w", err)
	}
	var errs []error
	if c.stack != nil {
		errs = append(errs, c.stack.Invalidate(ctx, partition(orgID), key(orgID, userID)))
	}
	if c.evict != nil {
		errs = append(errs, c.evict(ctx, orgID, userID))
	}
	return errors.Join(errs...)
}

// Close stops the stack's change-notice subscription. The shared layer's client
// belongs to whoever opened it.
func (c *Cache) Close() {
	if c.stack != nil {
		c.stack.Close()
	}
}

// canonical returns both ids in uuid's canonical spelling, or an error for an
// id that is not a UUID.
func canonical(orgID, userID string) (string, string, error) {
	org, orgErr := uuid.Parse(orgID)
	user, userErr := uuid.Parse(userID)
	if orgErr != nil || userErr != nil || org == uuid.Nil || user == uuid.Nil {
		return "", "", fmt.Errorf("membership: org and user must be non-nil UUIDs, got %q and %q", orgID, userID)
	}
	return org.String(), user.String(), nil
}

func partition(orgID string) cache.Partition {
	return cache.NewPartition("t:" + orgID)
}

func key(orgID, userID string) string {
	return keyPrefix + orgID + ":" + userID
}

// load reads one membership from the store.
func load(ctx context.Context, store Store, orgID, userID string) (string, error) {
	m, err := store.GetOrgMembership(ctx, orgID, userID)
	if err != nil || m == nil {
		return "", err
	}
	return m.Role.String(), nil
}

// origin loads a key from the store. The stack runs a shared load with the
// context of the caller that started it, so the scope check is repeated here:
// the database identity the store binds must be the one the key names.
func origin(store Store) cache.Source {
	return cache.SourceFunc(func(ctx context.Context, k string, _ string) (cache.Entry, error) {
		orgID, userID, ok := strings.Cut(strings.TrimPrefix(k, keyPrefix), ":")
		if !ok || !strings.HasPrefix(k, keyPrefix) {
			return cache.Entry{}, fmt.Errorf("membership: malformed cache key %q", k)
		}
		if err := auth.RequireVerifiedDatabaseScope(ctx, orgID, userID); err != nil {
			return cache.Entry{}, err
		}
		m, err := store.GetOrgMembership(ctx, orgID, userID)
		if err != nil {
			return cache.Entry{}, err
		}
		if m == nil {
			return cache.Entry{}, cache.ErrNotFound
		}
		return cache.Entry{Value: []byte(m.Role.String())}, nil
	})
}
