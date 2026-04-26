package cache

import (
	"context"
	"time"
)

// OrgMembership is the cached shape for "user X in org Y has role Z".
// The zero value (role="") means "not a member" and is cached separately
// so we also benefit from negative lookups (most members of org X are
// NOT in org Y — those negative answers are worth caching too).
type OrgMembership struct {
	Role string // "owner" | "admin" | "member" | "" when not a member
}

// OrgMembershipCache wraps a Cache with the key format + TTL for org
// membership lookups. Hot path: every authorized endpoint checks this
// — caching it collapses a per-request ListOrgMembers round trip into
// a single Redis GET.
type OrgMembershipCache struct {
	c   Cache
	ttl time.Duration
}

// NewOrgMembershipCache. Default TTL is 30s which is a deliberate
// trade-off: short enough that role changes take effect fast (a user
// promoted to admin sees it within 30s of the mutation even without
// explicit invalidation), long enough that a browser session issuing
// ~1 request/second only hits the DB twice a minute.
//
// Invalidation hooks (AddOrgMember/RemoveOrgMember) bring that down to
// "immediate" on mutation — TTL is just the ceiling.
func NewOrgMembershipCache(c Cache) *OrgMembershipCache {
	return &OrgMembershipCache{c: c, ttl: 30 * time.Second}
}

// key encodes the (org, user) tuple as a fully-qualified scoped path.
// Format: "t:<orgID>:u:<userID>:orgmember"
//
// Why both prefixes (and at the front): the typed-cache layer is the
// last-mile defense for tenant + user isolation. Putting the tenant
// AND user identifiers at the start of every key means a bug
// elsewhere can't read or write into the wrong tenant's keyspace,
// and TenantPrefix / UserPrefix are the same constants used by the
// generic Scoped() wrapper, so the boundary is grep-able and
// consistent across all per-tenant typed caches.
func (o *OrgMembershipCache) key(orgID, userID string) string {
	return TenantPrefix(orgID) + ":" + UserPrefix(userID) + ":orgmember"
}

// Get returns the cached membership. ErrNotFound means "not in cache,
// go ask the DB" — NOT "user has no membership" (that would be an empty
// Role in a cached entry).
func (o *OrgMembershipCache) Get(ctx context.Context, orgID, userID string) (*OrgMembership, error) {
	b, err := o.c.Get(ctx, o.key(orgID, userID))
	if err != nil {
		return nil, err
	}
	// One-byte encoding: role string is stored directly. Keeps the
	// serialization cost near zero; if we ever need more fields we'll
	// switch to json/proto.
	return &OrgMembership{Role: string(b)}, nil
}

// Set caches a membership record. Role can be "" to cache a known
// negative lookup ("confirmed NOT a member of this org").
func (o *OrgMembershipCache) Set(ctx context.Context, orgID, userID string, m *OrgMembership) error {
	return o.c.Set(ctx, o.key(orgID, userID), []byte(m.Role), o.ttl)
}

// Invalidate clears a single (org, user) entry. Called on every
// mutation that changes membership (add/remove/role-change) so readers
// see fresh data immediately instead of waiting out the TTL.
func (o *OrgMembershipCache) Invalidate(ctx context.Context, orgID, userID string) error {
	return o.c.Delete(ctx, o.key(orgID, userID))
}

// InvalidateOrg clears ALL members of an org. Use when the org is deleted
// or when a bulk change (e.g. cascade-remove from org) happens. Relies on
// the underlying Cache's Delete being able to take many keys, so the
// caller must supply the user IDs; there's no prefix-scan here because
// prefix scans on Redis are expensive and we always know the members.
func (o *OrgMembershipCache) InvalidateOrg(ctx context.Context, orgID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		keys = append(keys, o.key(orgID, uid))
	}
	return o.c.Delete(ctx, keys...)
}
