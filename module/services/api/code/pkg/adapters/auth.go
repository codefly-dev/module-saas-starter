package adapters

// Authorization helpers shared by every gRPC handler in this package.
//
// Before this file existed, each handler either duplicated the
// `wool.Get(ctx).UserAuthID()` dance or — worse — forgot to do it and
// shipped an endpoint with no auth at all (the security audit flagged
// 15+ such CRITICAL holes in list/get endpoints across User, Org, Team,
// APIKey, Audit). These helpers centralize the pattern so new handlers
// stay secure by default.
//
// Usage:
//
//	actorID, err := requireAuth(ctx)
//	if err != nil {
//	    return nil, err
//	}
//	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
//	    return nil, err
//	}
//
// The Service facade exposes ListOrgMembers / GetPlatformRole so these
// helpers can answer membership questions without a dedicated query path.

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/wool"

	"api/pkg/cache"
	"api/pkg/gen"
)

// orgMembershipCache is wired from adapters.WithService when the cache
// service is reachable. Nil means "no cache" — all auth helpers fall
// back to a direct DB call with no behavior change. Never short-circuit
// a cache error to a 5xx; the DB is always truth.
var orgMembershipCache *cache.OrgMembershipCache

// rateLimiter is the shared per-key request-budget tracker. Wired in
// work.go (or stays nil when no Redis is configured, in which case
// the rate-limit interceptor falls through to allow-all).
var rateLimiter *cache.RateLimiter

// WithRateLimiter installs the rate limiter. Idempotent — pass nil
// to disable. Only safe to call before the gRPC + Connect servers
// register their interceptors (which the work.go boot order does).
func WithRateLimiter(r *cache.RateLimiter) { rateLimiter = r }

// WithOrgMembershipCache lets the main() wire a cache after the service
// is constructed. Separate from WithService so caching is opt-in: a
// dev running without Redis just doesn't call this.
func WithOrgMembershipCache(c *cache.OrgMembershipCache) {
	orgMembershipCache = c
}

// lookupMembership consults cache first, falls back to DB, and fills
// the cache on DB hit. Returns (role, err) where role == "" + err == nil
// means "verified not-a-member" (still cached as a negative entry).
//
// The DB read goes through WithOrgTx scoped to orgID — that's the
// natural scope for "is this user a member of THIS org", and lets
// the org_members RLS policy through. We don't use bypass here on
// purpose: a typo in orgID (or a probe with someone else's org)
// SHOULD return zero rows, which the cache then memoizes as
// "not-a-member" — fail-closed.
func lookupMembership(ctx context.Context, orgID, userID string) (string, error) {
	if orgMembershipCache != nil {
		if m, err := orgMembershipCache.Get(ctx, orgID, userID); err == nil {
			return m.Role, nil
		}
		// ErrNotFound or any cache error → fall through to DB. We don't
		// distinguish: the DB is always truth, and cache failures should
		// never block an authorized request.
	}
	var members []*gen.OrgMembership
	if err := service.Store().WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ms, err := service.Store().ListOrgMembers(ctx, orgID)
		members = ms
		return err
	}); err != nil {
		return "", err
	}
	var role string
	for _, m := range members {
		if m.UserId == userID {
			role = m.Role.String()
			break
		}
	}
	if orgMembershipCache != nil {
		// Cache negative lookups too — saves DB hits for "not a member"
		// probes that would otherwise bypass the cache on every request.
		_ = orgMembershipCache.Set(ctx, orgID, userID, &cache.OrgMembership{Role: role})
	}
	return role, nil
}

// CacheInvalidator adapts the package-level orgMembershipCache to the
// business.MembershipInvalidator interface. Inject into Service via
// service.SetMembershipInvalidator(NewCacheInvalidator()) in main().
type CacheInvalidator struct{}

// NewCacheInvalidator returns an invalidator that wires through to the
// package-level cache. Safe to construct even when no cache is wired —
// the call will just be a no-op.
func NewCacheInvalidator() *CacheInvalidator { return &CacheInvalidator{} }

func (*CacheInvalidator) InvalidateMembership(ctx context.Context, orgID, userID string) {
	if orgMembershipCache == nil {
		return
	}
	_ = orgMembershipCache.Invalidate(ctx, orgID, userID)
}

// requireAuth extracts and validates the caller's user id from gRPC
// metadata (populated by the auth sidecar). Returns codes.Unauthenticated
// if the caller is anonymous.
func requireAuth(ctx context.Context) (string, error) {
	w := wool.Get(ctx)
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found || actorID == "" {
		return "", status.Error(codes.Unauthenticated, "authentication required")
	}
	return actorID, nil
}

// requireOrgMember verifies that actorID is a member of orgID. Returns
// codes.PermissionDenied otherwise. Uses the org-membership cache if
// configured (30s TTL + mutation-driven invalidation) to collapse the
// per-request ListOrgMembers round trip into a single Redis GET.
func requireOrgMember(ctx context.Context, actorID, orgID string) error {
	if orgID == "" {
		return status.Error(codes.InvalidArgument, "org_id required")
	}
	role, err := lookupMembership(ctx, orgID, actorID)
	if err != nil {
		return status.Errorf(codes.Internal, "cannot verify membership: %v", err)
	}
	if role == "" {
		return status.Error(codes.PermissionDenied, "not a member of this organization")
	}
	return nil
}

// requireOrgAdmin verifies that actorID has admin-or-owner role in orgID.
// Platform super_admin bypasses the check. Cache-backed same as above.
func requireOrgAdmin(ctx context.Context, actorID, orgID string) error {
	// Platform super_admin always passes.
	if role, err := service.Store().GetPlatformRole(ctx, actorID); err == nil && role == "super_admin" {
		return nil
	}
	if orgID == "" {
		return status.Error(codes.InvalidArgument, "org_id required")
	}
	role, err := lookupMembership(ctx, orgID, actorID)
	if err != nil {
		return status.Errorf(codes.Internal, "cannot verify membership: %v", err)
	}
	if role == "" {
		return status.Error(codes.PermissionDenied, "not a member of this organization")
	}
	// Role is the enum's String() form, e.g. "ORG_ROLE_ADMIN".
	if role == gen.OrgRole_ORG_ROLE_ADMIN.String() || role == gen.OrgRole_ORG_ROLE_OWNER.String() {
		return nil
	}
	return status.Error(codes.PermissionDenied, "requires org admin or owner role")
}

// requireTeamAdmin verifies actorID has admin-or-owner role within
// the team. Platform super_admin bypasses. team_members is RLS-
// protected (Phase 2C) and the policy JOINs to teams (also RLS), so
// the lookup has to enter WithOrgTx scoped to the team's owning
// org. We resolve team→org under WithBypass first since the auth
// helper is the entry point and doesn't yet have an org context.
//
// On success returns the resolved orgID; rpcs.go handlers stamp
// that onto ctx via business.WithCachedTeamOrgID so downstream
// Service methods (AddTeamMember etc.) skip a redundant WithBypass
// → 3 transactions per request instead of 4.
func requireTeamAdmin(ctx context.Context, actorID, teamID string) (string, error) {
	if role, err := service.Store().GetPlatformRole(ctx, actorID); err == nil && role == "super_admin" {
		// Even for super_admin we still need to resolve the org so
		// the downstream WithOrgTx is correctly scoped.
		var orgID string
		if err := service.Store().WithBypass(ctx, func(ctx context.Context) error {
			o, err := service.Store().GetTeamOrgID(ctx, teamID)
			orgID = o
			return err
		}); err != nil {
			return "", status.Errorf(codes.Internal, "cannot resolve team org: %v", err)
		}
		return orgID, nil
	}
	if teamID == "" {
		return "", status.Error(codes.InvalidArgument, "team_id required")
	}
	var orgID string
	if err := service.Store().WithBypass(ctx, func(ctx context.Context) error {
		o, err := service.Store().GetTeamOrgID(ctx, teamID)
		orgID = o
		return err
	}); err != nil {
		return "", status.Errorf(codes.Internal, "cannot resolve team org: %v", err)
	}
	if orgID == "" {
		return "", status.Error(codes.PermissionDenied, "not a member of this team")
	}
	var members []*gen.TeamMembership
	if err := service.Store().WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ms, err := service.Store().ListTeamMembers(ctx, teamID)
		members = ms
		return err
	}); err != nil {
		return "", status.Errorf(codes.Internal, "cannot verify team membership: %v", err)
	}
	for _, m := range members {
		if m.UserId != actorID {
			continue
		}
		if m.Role == gen.TeamRole_TEAM_ROLE_ADMIN || m.Role == gen.TeamRole_TEAM_ROLE_OWNER {
			return orgID, nil
		}
		return "", status.Error(codes.PermissionDenied, "requires team admin or owner role")
	}
	return "", status.Error(codes.PermissionDenied, "not a member of this team")
}

// requirePlatformAdmin verifies actorID has some platform role (support,
// billing, or super_admin). Used for endpoints that should only be
// reachable from the admin console.
func requirePlatformAdmin(ctx context.Context, actorID string) error {
	role, err := service.Store().GetPlatformRole(ctx, actorID)
	if err != nil {
		return status.Errorf(codes.Internal, "cannot resolve platform role: %v", err)
	}
	if role == "" {
		return status.Error(codes.PermissionDenied, "requires platform admin role")
	}
	return nil
}

// internalToken is the shared secret the auth-sidecar (and other
// trusted internal callers) carry to hit privileged-internal RPCs
// like CheckPermission. Set at startup from CODEFLY_INTERNAL_TOKEN.
// Empty = unset = dev mode (gate falls through, preserving the
// stand-alone run UX).
//
// Why a shared secret rather than mTLS: the codefly mesh today
// terminates network identity at the host level; per-process certs
// would require a substantial codefly-side change. A shared secret
// lets us draw a "auth-sidecar can call CheckPermission, anonymous
// cannot" line without that overhaul. mTLS is the eventual fix.
var internalToken string

// SetInternalToken installs the shared secret. Called once from
// work.go after reading CODEFLY_INTERNAL_TOKEN. Idempotent.
func SetInternalToken(token string) { internalToken = token }

// requireInternalOrAuth gates a privileged-internal RPC. Caller
// must present EITHER a valid JWT (standard auth path) OR a
// matching X-Codefly-Internal-Token header.
//
// Dev mode (internalToken=="") permits unauthenticated callers so
// `codefly run service api --stand-alone` still functions. Any
// deploy that sets CODEFLY_INTERNAL_TOKEN gets the real gate.
func requireInternalOrAuth(ctx context.Context) error {
	if id, ok := wool.Get(ctx).UserAuthID(); ok && id != "" {
		return nil
	}
	if internalToken == "" {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		for _, v := range md.Get("x-codefly-internal-token") {
			if v != "" && v == internalToken {
				return nil
			}
		}
	}
	return status.Error(codes.Unauthenticated, "internal endpoint requires JWT or X-Codefly-Internal-Token")
}

// requireSelfOrPlatformAdmin allows an action if the actor is either
// operating on themselves or is a platform admin. Used for endpoints
// like DeleteUser / GetUser.
func requireSelfOrPlatformAdmin(ctx context.Context, actorID, targetID string) error {
	if actorID == targetID {
		return nil
	}
	return requirePlatformAdmin(ctx, actorID)
}

// mfaSatisfiedCtxKey marks a context that came from a JWT bearing
// `mfa=true`. The Connect/gRPC auth interceptors stamp it after a
// successful VerifyAccess; consumers read it via mfaSatisfied(ctx).
type mfaSatisfiedCtxKeyType struct{}

var mfaSatisfiedCtxKey = mfaSatisfiedCtxKeyType{}

// scopesCtxKey holds the comma-separated scope list forwarded by the
// auth-sidecar (`X-Scopes`) when the caller authenticated with an API
// key. Empty when the caller used a JWT (interactive session) — JWT
// auth is gated by RBAC helpers (requireOrgAdmin etc.), not scopes.
type scopesCtxKeyType struct{}

var scopesCtxKey = scopesCtxKeyType{}

// withScopes stamps the parsed scope set on the context. Called by the
// Connect/gRPC interceptors when an X-Scopes header is present.
func withScopes(ctx context.Context, scopes []string) context.Context {
	if len(scopes) == 0 {
		return ctx
	}
	return context.WithValue(ctx, scopesCtxKey, scopes)
}

// scopesFromContext returns the parsed scope list, or nil when the
// caller did not authenticate with an API key.
func scopesFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(scopesCtxKey).([]string)
	return v
}

// requireScope enforces that an API-key caller has the required scope.
// JWT-authenticated callers (interactive sessions) bypass — their
// authorization comes from RBAC, not scope strings.
//
// Scope syntax: `resource:action`, e.g. `users:write`, `orgs:read`,
// `webhooks:write`, `*:*` (root). Wildcard `*` matches anything in
// either segment: `users:*` matches `users:read` and `users:write`,
// `*:read` matches read-only access across resources.
//
// Used by handlers that should be reachable both from interactive
// users (already gated by RBAC) and from API keys (need scope match).
// Returns codes.PermissionDenied with reason "missing_scope" so
// callers can branch.
func requireScope(ctx context.Context, required string) error {
	scopes := scopesFromContext(ctx)
	if len(scopes) == 0 {
		// No X-Scopes header → JWT auth path. RBAC has already gated.
		return nil
	}
	for _, s := range scopes {
		if scopeMatches(s, required) {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied, "missing_scope: %s", required)
}

// scopeMatches reports whether `granted` (a scope string from the
// caller's API key) covers `required` (the scope the handler asked
// for). Wildcards in granted are honored: `*:*` covers everything,
// `users:*` covers `users:read`/`users:write`, `*:read` covers any
// read across resources. Required is matched literally.
func scopeMatches(granted, required string) bool {
	if granted == required {
		return true
	}
	gParts := splitScope(granted)
	rParts := splitScope(required)
	if len(gParts) != 2 || len(rParts) != 2 {
		return false
	}
	if gParts[0] != "*" && gParts[0] != rParts[0] {
		return false
	}
	if gParts[1] != "*" && gParts[1] != rParts[1] {
		return false
	}
	return true
}

func splitScope(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

// parseScopes splits a comma-separated scope list (X-Scopes header
// payload) into a trimmed slice. Empty / whitespace-only entries are
// dropped. Stable order preserved so wildcard scopes evaluated first
// in api keys still take precedence on first match.
func parseScopes(raw string) []string {
	if raw == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == ',' {
			tok := trimSpace(raw[start:i])
			if tok != "" {
				out = append(out, tok)
			}
			start = i + 1
		}
	}
	return out
}

// trimSpace is a tiny inline alternative to strings.TrimSpace to avoid
// pulling the strings import into auth.go just for one call.
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// withMFASatisfied returns a context flagged as MFA-satisfied. Called
// only by the auth interceptors after parsing a valid bearer token.
func withMFASatisfied(ctx context.Context) context.Context {
	return context.WithValue(ctx, mfaSatisfiedCtxKey, true)
}

// mfaSatisfied reports whether this request rode on a JWT minted after
// the user cleared an MFA challenge (or whose user has no MFA device).
func mfaSatisfied(ctx context.Context) bool {
	v, _ := ctx.Value(mfaSatisfiedCtxKey).(bool)
	return v
}

// requireMFA gates sensitive operations behind a satisfied MFA challenge
// when the actor has at least one MFA device enrolled. Users who never
// set up MFA still pass — enforcement is opt-in per user, not platform-
// wide. Returns codes.FailedPrecondition with the canonical
// "mfa_required" reason so frontends can branch on it without parsing
// strings.
//
// Apply to: anything that, if hijacked, would let an attacker pivot to
// admin scope, exfiltrate data, or move money. Concretely: billing
// checkout/portal, role grants, GDPR delete, impersonation, API key
// creation with broad scopes.
//
// The check is fast: a single DB hit (or its cached equivalent in the
// future) only when the session DIDN'T satisfy MFA — happy path is
// just a context value read.
func requireMFA(ctx context.Context, actorID string) error {
	if mfaSatisfied(ctx) {
		return nil
	}
	// mfa_devices is RLS-protected by user_id (Phase 2G); wrap in
	// the actor's WithUserTx so the count query sees their devices.
	var enrolled bool
	if err := service.Store().WithUserTx(ctx, actorID, func(ctx context.Context) error {
		e, err := service.Store().HasVerifiedMFA(ctx, actorID)
		enrolled = e
		return err
	}); err != nil {
		// Fail closed when the lookup itself fails — better to block a
		// sensitive op than to default-allow.
		return status.Errorf(codes.Internal, "cannot verify MFA status: %v", err)
	}
	if !enrolled {
		// No verified MFA device → can't enforce. Sensitive ops still
		// run; audit captures mfa_satisfied=false. Operators who want
		// to force-enroll go through a separate "MFA required" feature
		// flag at the org level (not yet implemented).
		return nil
	}
	return status.Error(codes.FailedPrecondition, "mfa_required")
}

// errPermissionDenied is the canonical error shape callers can check.
var errPermissionDenied = errors.New("permission denied")

// IsPermissionDenied reports whether err is a gRPC PermissionDenied or
// the sentinel above. Useful in aggregated handlers that need to decide
// between 403 and 500.
func IsPermissionDenied(err error) bool {
	if errors.Is(err, errPermissionDenied) {
		return true
	}
	if s, ok := status.FromError(err); ok {
		return s.Code() == codes.PermissionDenied
	}
	return false
}
