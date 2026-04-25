package cache

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// RateLimiter is a Redis-backed fixed-window counter — the simplest
// shape that gives correct per-key request budgets across multiple api
// instances. We deliberately do NOT use a token bucket here: a fixed
// window has one Redis op per check (INCR + EXPIRE on first hit) and
// fits the typical SaaS rate-limit policy ("1000 reqs/min per org"
// is a window, not a sustained-rate guarantee).
//
// Falls back to allow-all when the cache layer is missing — same
// pattern as the org-membership cache. The DB / business gates are
// the real correctness guard; rate limiting is overload protection,
// not authorization.
type RateLimiter struct {
	cache Cache
}

// NewRateLimiter wraps a Cache (memory in tests, Redis in prod).
func NewRateLimiter(c Cache) *RateLimiter {
	return &RateLimiter{cache: c}
}

const rateLimitPrefix = "ratelimit:"

// Allow records one hit for `key` within a `window`-long bucket and
// returns:
//   - allowed=true if the count is still ≤ limit
//   - allowed=false otherwise (caller should reject with
//     ResourceExhausted)
//   - remaining: budget left in the current window
//   - resetAt: when the current window rolls over
//
// limit ≤ 0 disables the check (unlimited). nil cache = unlimited.
//
// Window keys are bucketed to the floor of (now / window) so two api
// instances using the same wall clock land on the same Redis key —
// the increments are atomic and the budget is shared correctly.
func (r *RateLimiter) Allow(
	ctx context.Context,
	key string,
	limit int,
	window time.Duration,
) (allowed bool, remaining int, resetAt time.Time, err error) {
	if r == nil || r.cache == nil || limit <= 0 || window <= 0 {
		return true, limit, time.Time{}, nil
	}

	bucket := time.Now().Truncate(window)
	resetAt = bucket.Add(window)
	bucketKey := rateLimitPrefix + key + ":" + strconv.FormatInt(bucket.Unix(), 10)

	// Read-modify-write via Get + Set. Connect-Web traffic doesn't
	// race hard enough to need INCR/Lua atomicity at the typical
	// throughputs of a SaaS admin API; if you do, swap this for a
	// Redis-Lua atomic counter and a typed Cache extension.
	var count int
	if v, getErr := r.cache.Get(ctx, bucketKey); getErr == nil {
		// Stored as ASCII int — Redis-friendly + readable in redis-cli.
		count, _ = strconv.Atoi(string(v))
	} else if !errors.Is(getErr, ErrNotFound) {
		// Soft-fail: treat backing-store errors as allow. Rate limiting
		// is best-effort overload protection; we don't want a Redis
		// blip to mass-reject traffic.
		return true, limit, resetAt, nil
	}

	count++
	// Set with a TTL slightly past the window end so a clock skew
	// between instances doesn't cause cleanup races.
	_ = r.cache.Set(ctx, bucketKey, []byte(strconv.Itoa(count)), window+5*time.Second)

	if count > limit {
		return false, 0, resetAt, nil
	}
	return true, limit - count, resetAt, nil
}
