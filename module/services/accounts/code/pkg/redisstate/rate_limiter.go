package redisstate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrCounterUnavailable is Allow's error when a budget is set and the hit
// could not be counted: the backing store failed, or there is none. The call
// is not allowed. A caller that admitted it would turn every budget off for as
// long as the store is down, which is the moment a flood is most likely and
// least visible.
var ErrCounterUnavailable = errors.New("rate limit counter unavailable")

// RateLimiter is a Redis-backed fixed-window counter — the simplest
// shape that gives correct per-key request budgets across multiple api
// instances. We deliberately do NOT use a token bucket here: a fixed
// window has one Redis op per check (INCR + EXPIRE on first hit) and
// fits the typical SaaS rate-limit policy ("1000 reqs/min per org"
// is a window, not a sustained-rate guarantee).
//
// Fails closed: a hit that cannot be counted is refused with
// ErrCounterUnavailable, whether the store answered with an error or was never
// wired. The budgets guard sign-in, registration and the authority's checks,
// so "the counter is down" must not mean "there is no budget".
type RateLimiter struct {
	state State
}

// NewRateLimiter wraps State (memory in tests, Redis in prod).
func NewRateLimiter(s State) *RateLimiter {
	return &RateLimiter{state: s}
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
// limit ≤ 0 or window ≤ 0 is a caller that set no budget (unlimited). With a
// budget set, a nil limiter, a nil state or a failing store returns
// allowed=false and an error matching ErrCounterUnavailable.
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
	if limit <= 0 || window <= 0 {
		return true, limit, time.Time{}, nil
	}
	if r == nil || r.state == nil {
		return false, 0, time.Time{}, fmt.Errorf("%w: no counter is wired", ErrCounterUnavailable)
	}

	bucket := time.Now().Truncate(window)
	resetAt = bucket.Add(window)
	bucketKey := rateLimitPrefix + key + ":" + strconv.FormatInt(bucket.Unix(), 10)

	// Atomic INCR + first-hit EXPIRE. A concurrent burst that hits several
	// api instances at once still lands one increment per request against
	// the shared counter, so the budget can't be overspent by a Get/Set
	// read-modify-write race. TTL runs slightly past the window end so
	// clock skew between instances doesn't evict a live bucket early.
	count, err := r.state.Incr(ctx, bucketKey, window+5*time.Second)
	if err != nil {
		return false, 0, resetAt, fmt.Errorf("%w: %w", ErrCounterUnavailable, err)
	}

	if count > int64(limit) {
		return false, 0, resetAt, nil
	}
	return true, limit - int(count), resetAt, nil
}
