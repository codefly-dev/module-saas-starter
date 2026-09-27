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
// across processes); an invalidation on any replica evicts every replica's
// in-process copy (the shared layer is a Notifier); an unreachable Redis falls
// through to the store behind a breaker; and "not a member" is cached as a
// negative entry.
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
// generation across every partition: the key alone must identify the fact.
package membership

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cache "github.com/codefly-dev/interface-cache/go/cache"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

const (
	// memoryTTL bounds the in-process copy. Invalidations reach it through the
	// shared layer's change notices; the TTL only bounds how stale a copy can be
	// if a notice is lost without a resync.
	memoryTTL = 5 * time.Second
	// sharedTTL is the ceiling on how long a membership change can go unseen
	// when no invalidation runs (a write that bypasses the business layer). It
	// is the 30s the previous Redis cache used; explicit invalidation on every
	// membership mutation brings it down to immediate.
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

// Cache is org membership behind a cache stack. Safe for concurrent use.
type Cache struct {
	stack *cache.Stack
}

// New builds the stack over shared, with store as its origin. shared is the
// layer every replica reaches: the Redis driver in a running service, a
// cache.Memory (Share()d between instances) in tests. New subscribes to
// shared's change notices, so it fails when shared cannot be reached.
func New(ctx context.Context, shared cache.Layer, store Store) (*Cache, error) {
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
	return &Cache{stack: stack}, nil
}

// Role returns userID's role in orgID, or "" when the user is verifiably not a
// member. The caller's verified identity must be exactly (orgID, userID): the
// cache stores data, never decisions, so the same scope check that guards the
// store read runs before any layer is consulted.
func (c *Cache) Role(ctx context.Context, orgID, userID string) (string, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, orgID, userID); err != nil {
		return "", err
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
func (c *Cache) Invalidate(ctx context.Context, orgID, userID string) error {
	if orgID == "" || userID == "" {
		return errors.New("membership: Invalidate needs an org and a user")
	}
	return c.stack.Invalidate(ctx, partition(orgID), key(orgID, userID))
}

// InvalidateOrg invalidates each of userIDs in orgID. A namespace-wide flush
// would not need the member list (codefly-dev/interface-cache#4 §5); until the
// interface has one, this is a loop, and every failure is reported.
func (c *Cache) InvalidateOrg(ctx context.Context, orgID string, userIDs []string) error {
	var errs []error
	for _, userID := range userIDs {
		if err := c.Invalidate(ctx, orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close stops the stack's change-notice subscription. The shared layer's client
// belongs to whoever opened it.
func (c *Cache) Close() { c.stack.Close() }

func partition(orgID string) cache.Partition {
	return cache.NewPartition("t:" + orgID)
}

func key(orgID, userID string) string {
	return keyPrefix + orgID + ":" + userID
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
