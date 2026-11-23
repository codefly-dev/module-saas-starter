package adapters

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"accounts/pkg/business"
	"accounts/pkg/redisstate"

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
//
// A hit the limiter cannot count is refused, not admitted: Unavailable
// (HTTP 503) with a Retry-After of rateLimitUnavailableRetry, and one WARN
// line naming the cause. Admitting it would lift every budget, sign-in and
// registration included, for as long as the counter's store is down.
//
// A workload's call that carries its proof in the request (the authority's
// Check, Keys and the workload-credential procedures) names no session, so
// it has none of the identifiers above. It is budgeted per source address
// in a bucket of its own, at machineLimit: one person's action costs one
// such call per hop it crosses, every workload of a cell shares an address
// behind a mesh or on loopback, and charging them to the anonymous
// visitor's budget refused every module's reads once a busy minute spent it.
type rateLimitConfig struct {
	defaultLimit  int
	machineLimit  int
	defaultWindow time.Duration
}

func defaultRateLimitConfig() rateLimitConfig {
	return rateLimitConfig{
		defaultLimit:  1000,
		machineLimit:  60000,
		defaultWindow: time.Minute,
	}
}

// rateLimitBudget is the bucket a call is counted in and its size.
type rateLimitBudget struct {
	key   string
	limit int
}

// rateLimitInterceptor builds the Connect interceptor. A nil limiter is a
// chain built without one, which only tests do: the service's start-up wires
// the limiter unconditionally or fails. A limiter that is wired and cannot
// count refuses.
func rateLimitInterceptor(limiter *redisstate.RateLimiter) connect.UnaryInterceptorFunc {
	cfg := defaultRateLimitConfig()
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if limiter == nil {
				return next(ctx, req)
			}
			budget := rateLimitBudgetFor(cfg, rateLimitKeyFromCtx(ctx), req.Spec().Procedure,
				clientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For")))
			if budget.key == "" {
				// Only when even the peer address is unparseable — the
				// budget needs a key to bucket on. Anonymous callers
				// otherwise fall through to a per-IP key below.
				return next(ctx, req)
			}
			allowed, remaining, resetAt, err := limiter.Allow(
				ctx, budget.key, budget.limit, cfg.defaultWindow,
			)
			if err != nil {
				wool.Get(ctx).In("rateLimitInterceptor").
					Warn(rateLimitUnavailableLog, wool.ErrField(err), wool.Field("procedure", req.Spec().Procedure))
				cerr := connect.NewError(connect.CodeUnavailable, errors.New(rateLimitUnavailableMessage))
				if detail, err := connect.NewErrorDetail(&errdetails.RetryInfo{RetryDelay: durationpb.New(rateLimitUnavailableRetry)}); err == nil {
					cerr.AddDetail(detail)
				}
				cerr.Meta().Set("Retry-After", strconv.Itoa(int(rateLimitUnavailableRetry.Seconds())))
				return nil, cerr
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
				// gRPC clients of this Connect listener receive status details.
				if detail, err := connect.NewErrorDetail(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)}); err == nil {
					cerr.AddDetail(detail)
				}
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
					strconv.Itoa(budget.limit))
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
// Same key derivation, same refusals; the package-level
// `rateLimiter` is shared so a single org budget covers Connect AND
// gRPC traffic together rather than per-protocol.
func grpcRateLimitInterceptor() grpc.UnaryServerInterceptor {
	cfg := defaultRateLimitConfig()
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if rateLimiter == nil {
			return handler(ctx, req)
		}
		procedure := ""
		if info != nil {
			procedure = info.FullMethod
		}
		budget := rateLimitBudgetFor(cfg, rateLimitKeyFromCtx(ctx), procedure,
			clientIP(grpcPeerAddr(ctx), grpcForwardedFor(ctx)))
		if budget.key == "" {
			return handler(ctx, req)
		}
		allowed, _, resetAt, err := rateLimiter.Allow(
			ctx, budget.key, budget.limit, cfg.defaultWindow,
		)
		if err != nil {
			wool.Get(ctx).In("grpcRateLimitInterceptor").
				Warn(rateLimitUnavailableLog, wool.ErrField(err), wool.Field("procedure", procedure))
			refusal := status.New(codes.Unavailable, rateLimitUnavailableMessage)
			hint := &errdetails.RetryInfo{RetryDelay: durationpb.New(rateLimitUnavailableRetry)}
			if detailed, err := refusal.WithDetails(hint); err == nil {
				refusal = detailed
			}
			return nil, refusal.Err()
		}
		if !allowed {
			refusal := status.New(codes.ResourceExhausted, ErrRateLimited.Error())
			hint := &errdetails.RetryInfo{RetryDelay: durationpb.New(max(0, time.Until(resetAt)))}
			if detailed, err := refusal.WithDetails(hint); err == nil {
				refusal = detailed
			}
			return nil, refusal.Err()
		}
		return handler(ctx, req)
	}
}

// rateLimitBudgetFor picks the bucket for one call: the caller's verified
// identity when it has one; otherwise its source address, in the machine
// bucket for a procedure a workload proves itself to in the request and in
// the anonymous bucket for everything else (Authenticate, BeginOAuth,
// RegisterUser, JoinWaitlist). The key is "" only when the peer address
// cannot be parsed either.
func rateLimitBudgetFor(cfg rateLimitConfig, identity, procedure, ip string) rateLimitBudget {
	if identity != "" {
		return rateLimitBudget{key: identity, limit: cfg.defaultLimit}
	}
	if ip == "" {
		return rateLimitBudget{}
	}
	if business.CredentialInRequest(procedure) {
		return rateLimitBudget{key: "machine:" + ip, limit: cfg.machineLimit}
	}
	return rateLimitBudget{key: ipRateLimitKey(ip), limit: cfg.defaultLimit}
}

func ipRateLimitKey(ip string) string {
	if ip == "" {
		return ""
	}
	return "ip:" + ip
}

func grpcPeerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return ""
}

func grpcForwardedFor(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-forwarded-for"); len(vals) > 0 {
			return strings.Join(vals, ",")
		}
	}
	return ""
}

// rateLimitKeyFromCtx is the protocol-agnostic identity key derivation used
// by both the Connect and gRPC interceptors. Returns "" for callers with no
// verified identity.
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

// rateLimitUnavailableMessage is what a caller is told when its hit could not
// be counted. It names no store and no cause; the cause is in the log line.
const rateLimitUnavailableMessage = "rate limiting temporarily unavailable"

// rateLimitUnavailableLog is the one line each such refusal leaves.
const rateLimitUnavailableLog = "rate limit counter unavailable, refusing (fail-closed)"

// rateLimitUnavailableRetry is the delay a refused caller is asked to wait.
// The counter has no window to report, so this is a fixed, short pause.
const rateLimitUnavailableRetry = 5 * time.Second

// ErrRateLimited is the sentinel error returned to clients hit by
// the rate limiter. The string is intentionally short — Connect
// surfaces it as the gRPC error message which clients display.
var ErrRateLimited = errRateLimited{}

type errRateLimited struct{}

func (errRateLimited) Error() string { return "rate_limited" }
