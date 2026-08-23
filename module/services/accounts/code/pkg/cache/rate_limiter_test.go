package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accounts/pkg/cache"
)

func TestRateLimiter_AllowUntilLimit(t *testing.T) {
	rl := cache.NewRateLimiter(cache.NewMemory())
	ctx := context.Background()

	// 3 reqs/window. First 3 must pass; 4th must fail.
	for i := 1; i <= 3; i++ {
		allowed, remaining, _, err := rl.Allow(ctx, "k", 3, time.Minute)
		if err != nil {
			t.Fatalf("Allow #%d err: %v", i, err)
		}
		if !allowed {
			t.Errorf("req #%d should be allowed", i)
		}
		if remaining != 3-i {
			t.Errorf("req #%d remaining=%d, want %d", i, remaining, 3-i)
		}
	}
	allowed, remaining, _, _ := rl.Allow(ctx, "k", 3, time.Minute)
	if allowed {
		t.Errorf("req #4 should be rejected")
	}
	if remaining != 0 {
		t.Errorf("req #4 remaining=%d, want 0", remaining)
	}
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	// Org A burns its budget; Org B is unaffected.
	rl := cache.NewRateLimiter(cache.NewMemory())
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _, _, _ = rl.Allow(ctx, "org:A", 5, time.Minute)
	}
	if allowed, _, _, _ := rl.Allow(ctx, "org:A", 5, time.Minute); allowed {
		t.Errorf("org A 6th call should be rejected")
	}
	if allowed, _, _, _ := rl.Allow(ctx, "org:B", 5, time.Minute); !allowed {
		t.Errorf("org B first call should pass — separate budget")
	}
}

func TestRateLimiter_AtomicUnderConcurrentBurst(t *testing.T) {
	// A concurrent burst against one key must admit exactly `limit`
	// requests — the atomic INCR forbids the Get/Set race that would let
	// two goroutines read the same count and both write count+1, letting
	// the budget be overspent.
	rl := cache.NewRateLimiter(cache.NewMemory())
	ctx := context.Background()

	const limit = 100
	const callers = 500

	// An hour-long window guarantees every goroutine lands in the same
	// fixed-window bucket, so a boundary roll-over can't split the burst
	// across two counters and make the exact-count assertion flaky.
	const window = time.Hour

	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _, _, _ := rl.Allow(ctx, "burst", limit, window); ok {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != limit {
		t.Errorf("allowed %d requests, want exactly %d", got, limit)
	}
}

func TestRateLimiter_ZeroLimitIsUnlimited(t *testing.T) {
	rl := cache.NewRateLimiter(cache.NewMemory())
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if allowed, _, _, _ := rl.Allow(ctx, "k", 0, time.Minute); !allowed {
			t.Fatalf("limit=0 must allow unlimited; rejected at #%d", i)
		}
	}
}

func TestRateLimiter_NilCacheIsAllowAll(t *testing.T) {
	// Production fallback: if Redis is unwired, the limiter must
	// degrade gracefully to allow-all rather than mass-rejecting.
	rl := cache.NewRateLimiter(nil)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if allowed, _, _, _ := rl.Allow(ctx, "k", 1, time.Minute); !allowed {
			t.Fatalf("nil cache must allow-all; rejected at #%d", i)
		}
	}
}

func TestRateLimiter_NilReceiverIsAllowAll(t *testing.T) {
	// Defensive: forgetting to construct the limiter must NOT panic.
	var rl *cache.RateLimiter
	allowed, _, _, err := rl.Allow(context.Background(), "k", 1, time.Minute)
	if err != nil || !allowed {
		t.Errorf("nil receiver must allow without error; got allowed=%v err=%v", allowed, err)
	}
}
