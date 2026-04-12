package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RateLimiter implements per-key sliding-window rate limiting using in-memory
// counters. In production this should be backed by Redis (INCR + EXPIRE) for
// multi-instance consistency; the in-memory fallback is fine for local dev
// where only one gateway process runs.
type RateLimiter struct {
	windows sync.Map // key (org:minute) -> *window
	limit   int      // requests per minute
	burst   int      // burst allowance above limit
	stop    chan struct{}
}

// window tracks request count for a single key in a single minute.
type window struct {
	count   atomic.Int64
	resetAt time.Time
}

// NewRateLimiter creates a rate limiter allowing requestsPerMinute requests
// per key per minute, with a 20% burst allowance. It starts a background
// goroutine to clean up expired windows every 5 minutes.
func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	burst := requestsPerMinute / 5 // 20% burst
	if burst < 1 {
		burst = 1
	}
	rl := &RateLimiter{
		limit: requestsPerMinute,
		burst: burst,
		stop:  make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Allow checks whether the given key is under the rate limit for the current
// minute window. Returns whether the request is allowed, how many requests
// remain, and the Unix timestamp when the window resets.
func (rl *RateLimiter) Allow(key string) (allowed bool, remaining int, resetUnix int64) {
	now := time.Now()
	minuteKey := key + ":" + now.Truncate(time.Minute).Format("200601021504")
	resetAt := now.Truncate(time.Minute).Add(time.Minute)

	actual, _ := rl.windows.LoadOrStore(minuteKey, &window{resetAt: resetAt})
	w := actual.(*window)

	effective := int64(rl.limit + rl.burst)
	current := w.count.Add(1)
	if current > effective {
		rem := 0
		return false, rem, resetAt.Unix()
	}

	rem := int(effective - current)
	if rem < 0 {
		rem = 0
	}
	return true, rem, resetAt.Unix()
}

// Middleware returns an http.Handler that enforces rate limiting. It reads the
// X-Org-Id header (set by the sidecar after auth) to determine the rate limit
// key. If no org ID is present, it falls back to the client IP.
//
// On 429 Too Many Requests it sets standard rate limit headers:
//
//	Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Org-Id")
		keyKind := "org"
		if key == "" {
			key = clientIP(r)
			keyKind = "ip"
		}

		allowed, remaining, resetUnix := rl.Allow(key)

		// Always set informational rate limit headers.
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))

		if !allowed {
			retryAfter := resetUnix - time.Now().Unix()
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			log.Printf("rate limit exceeded: key=%s (%s), limit=%d/min", key, keyKind, rl.limit)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Stop terminates the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stop)
}

// cleanup periodically removes expired windows to prevent unbounded memory
// growth. Runs every 5 minutes until Stop is called.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			now := time.Now()
			rl.windows.Range(func(k, v any) bool {
				w := v.(*window)
				if now.After(w.resetAt) {
					rl.windows.Delete(k)
				}
				return true
			})
		}
	}
}

// clientIP extracts the client IP from the request, checking X-Forwarded-For
// first (for proxied requests), then falling back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client.
		if ip := strings.SplitN(xff, ",", 2)[0]; ip != "" {
			return strings.TrimSpace(ip)
		}
	}
	if xff := r.Header.Get("X-Real-Ip"); xff != "" {
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

