package adapters

import (
	"context"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/cache"

	"github.com/codefly-dev/core/wool"
)

// RateLimitInterceptor enforces per-org / per-API-key request budgets
// on the Connect transport. Goes after the auth interceptor so the
// caller's identity (UserID, OrgID, or API key) is already on the
// context — the limit key picks the most specific identifier
// available:
//
//  1. API key id   — preferred when the caller authenticated via
//     `cfly_sk_*` (sidecar set X-Auth-Id).
//  2. Org id       — when the caller is part of a specific tenant.
//  3. User id      — solo-user endpoints (no org context yet).
//  4. Remote IP    — anonymous endpoints (BeginOAuth, Authenticate).
//
// Defaults are intentionally generous (1000 req/min per org) — the
// gate exists to absorb runaway clients and cost-amplification bugs,
// not to shape user behaviour. Override per deployment via the
// limiter's policy hook (not yet exposed; one follow-up task).
//
// On reject, surfaces connect.CodeResourceExhausted with a Retry-After
// hint computed from the window reset — Connect propagates this to
// gRPC clients as code 8 and to HTTP clients as 429.
type rateLimitConfig struct {
	defaultLimit  int
	defaultWindow time.Duration
}

func defaultRateLimitConfig() rateLimitConfig {
	return rateLimitConfig{
		defaultLimit:  1000,
		defaultWindow: time.Minute,
	}
}

// rateLimitInterceptor builds the Connect interceptor. limiter may be
// nil (no Redis wired) — interceptor falls through to allow-all.
func rateLimitInterceptor(limiter *cache.RateLimiter) connect.UnaryInterceptorFunc {
	cfg := defaultRateLimitConfig()
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if limiter == nil {
				return next(ctx, req)
			}
			key := rateLimitKey(ctx, req)
			if key == "" {
				// No identifier we trust → skip rather than IP-block;
				// IP-block lives at the gateway/CDN layer where it
				// belongs in production.
				return next(ctx, req)
			}
			allowed, remaining, resetAt, err := limiter.Allow(
				ctx, key, cfg.defaultLimit, cfg.defaultWindow,
			)
			if err != nil {
				wool.Get(ctx).In("rateLimitInterceptor").
					Debug("limiter error (allowing)", wool.ErrField(err))
				return next(ctx, req)
			}
			if !allowed {
				retryAfter := time.Until(resetAt)
				if retryAfter < 0 {
					retryAfter = cfg.defaultWindow
				}
				cerr := connect.NewError(
					connect.CodeResourceExhausted,
					ErrRateLimited,
				)
				cerr.Meta().Set("Retry-After",
					strconv.Itoa(int(retryAfter.Seconds())+1))
				cerr.Meta().Set("X-RateLimit-Reset",
					strconv.FormatInt(resetAt.Unix(), 10))
				return nil, cerr
			}
			// Set RateLimit headers on success too so well-behaved
			// clients can self-throttle before they hit the wall.
			resp, err := next(ctx, req)
			if err == nil && resp != nil {
				resp.Header().Set("X-RateLimit-Limit",
					strconv.Itoa(cfg.defaultLimit))
				resp.Header().Set("X-RateLimit-Remaining",
					strconv.Itoa(remaining))
				resp.Header().Set("X-RateLimit-Reset",
					strconv.FormatInt(resetAt.Unix(), 10))
			}
			return resp, err
		}
	}
}

// grpcRateLimitInterceptor is the gRPC twin of rateLimitInterceptor.
// Same key derivation, same fall-through semantics; the package-level
// `rateLimiter` is shared so a single org budget covers Connect AND
// gRPC traffic together rather than per-protocol.
func grpcRateLimitInterceptor() grpc.UnaryServerInterceptor {
	cfg := defaultRateLimitConfig()
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if rateLimiter == nil {
			return handler(ctx, req)
		}
		key := rateLimitKeyFromCtx(ctx)
		if key == "" {
			return handler(ctx, req)
		}
		allowed, _, _, err := rateLimiter.Allow(
			ctx, key, cfg.defaultLimit, cfg.defaultWindow,
		)
		if err != nil {
			wool.Get(ctx).In("grpcRateLimitInterceptor").
				Debug("limiter error (allowing)", wool.ErrField(err))
			return handler(ctx, req)
		}
		if !allowed {
			return nil, status.Errorf(codes.ResourceExhausted, "rate_limited")
		}
		return handler(ctx, req)
	}
}

// rateLimitKey picks the most specific identifier available on the
// context. Returns "" when none is present so the caller skips the
// check (anonymous traffic is rare on this api — Authenticate /
// BeginOAuth are the only paths and they have their own provider-
// side rate limits).
func rateLimitKey(ctx context.Context, _ connect.AnyRequest) string {
	return rateLimitKeyFromCtx(ctx)
}

// rateLimitKeyFromCtx is the protocol-agnostic key derivation used by
// both the Connect and gRPC interceptors.
func rateLimitKeyFromCtx(ctx context.Context) string {
	w := wool.Get(ctx)
	if id, ok := w.UserAuthID(); ok && id != "" {
		// X-Auth-Id is the API-key id when the caller used cfly_sk_*;
		// for JWT users it's the user uuid. Either way it's the
		// finest-grained identifier.
		return "auth:" + id
	}
	if id, ok := w.OrgID(); ok && id != "" {
		return "org:" + id
	}
	if id, ok := w.UserID(); ok && id != "" {
		return "user:" + id
	}
	return ""
}

// ErrRateLimited is the sentinel error returned to clients hit by
// the rate limiter. The string is intentionally short — Connect
// surfaces it as the gRPC error message which clients display.
var ErrRateLimited = errRateLimited{}

type errRateLimited struct{}

func (errRateLimited) Error() string { return "rate_limited" }
