package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimitBackend is the strategy interface for counting requests.
type rateLimitBackend interface {
	// Increment atomically increments the counter for key and returns the
	// new count. If the key is new, it is initialised with a TTL of ttl.
	Increment(key string, ttl time.Duration) (count int64, err error)
	// Close releases resources (e.g. the Redis connection).
	Close() error
}

// -------------------------------------------------------------------------
// In-memory backend (default / local dev)
// -------------------------------------------------------------------------

type memoryBackend struct {
	windows sync.Map // key -> *window
}

type window struct {
	count   atomic.Int64
	resetAt time.Time
}

func (m *memoryBackend) Increment(key string, ttl time.Duration) (int64, error) {
	resetAt := time.Now().Add(ttl)
	actual, _ := m.windows.LoadOrStore(key, &window{resetAt: resetAt})
	w := actual.(*window)
	return w.count.Add(1), nil
}

func (m *memoryBackend) Close() error { return nil }

func (m *memoryBackend) cleanup(stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now()
			m.windows.Range(func(k, v any) bool {
				w := v.(*window)
				if now.After(w.resetAt) {
					m.windows.Delete(k)
				}
				return true
			})
		}
	}
}

// -------------------------------------------------------------------------
// Redis backend (production, multi-instance)
// -------------------------------------------------------------------------

// redisBackend uses go-redis's bounded connection pool, URL/TLS handling, and
// per-operation timeouts. redisScript keeps INCR + initial expiry atomic.
type redisBackend struct {
	client *redis.Client
}

var redisIncrementScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return current
`)

func newRedisBackend(rawURL string) (*redisBackend, error) {
	options, err := redisClientOptions(rawURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), options.DialTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping %s: %w", options.Addr, err)
	}
	return &redisBackend{client: client}, nil
}

func redisClientOptions(rawURL string) (*redis.Options, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "redis://" + rawURL
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL")
	}
	options.DialTimeout = 5 * time.Second
	options.ReadTimeout = 2 * time.Second
	options.WriteTimeout = 2 * time.Second
	options.PoolTimeout = 3 * time.Second
	options.PoolSize = 10
	options.MinIdleConns = 1
	options.MaxIdleConns = 5
	options.ConnMaxIdleTime = 5 * time.Minute
	if options.TLSConfig != nil {
		options.TLSConfig.MinVersion = tls.VersionTLS12
	}
	return options, nil
}

func (r *redisBackend) Increment(key string, ttl time.Duration) (int64, error) {
	ttlMillis := ttl.Milliseconds()
	if ttlMillis < 1 {
		ttlMillis = 1
	}
	return redisIncrementScript.Run(context.Background(), r.client, []string{key}, ttlMillis).Int64()
}

func (r *redisBackend) Close() error {
	return r.client.Close()
}

// -------------------------------------------------------------------------
// RateLimiter (backend-agnostic)
// -------------------------------------------------------------------------

// RateLimiter implements per-key fixed-window rate limiting.
//
// If REDIS_URL is set, it uses Redis (atomic INCR + initial expiry) for multi-instance
// consistency. Otherwise it falls back to an in-memory backend that is
// fine for local dev where only one process runs.
type RateLimiter struct {
	backend                    rateLimitBackend
	limit                      int // requests per minute
	burst                      int // burst allowance above limit
	authenticationAttemptLimit int
	stop                       chan struct{}
	proxies                    proxyTrust
}

type rateLimiterOptions struct {
	redisURL                   string
	authenticationAttemptLimit int
}

type RateLimiterOption func(*rateLimiterOptions)

func WithRedisURL(redisURL string) RateLimiterOption {
	return func(options *rateLimiterOptions) { options.redisURL = redisURL }
}

func WithAuthenticationAttemptLimit(limit int) RateLimiterOption {
	return func(options *rateLimiterOptions) { options.authenticationAttemptLimit = limit }
}

// NewRateLimiter creates a rate limiter allowing requestsPerMinute requests
// per key per minute, with a 20% burst allowance. If REDIS_URL is set it
// connects to Redis; otherwise it uses in-memory counters with a background
// cleanup goroutine.
func NewRateLimiter(requestsPerMinute int, optionFns ...RateLimiterOption) *RateLimiter {
	options := rateLimiterOptions{
		redisURL:                   os.Getenv("REDIS_URL"),
		authenticationAttemptLimit: authenticationAttemptLimitPerMinute,
	}
	for _, option := range optionFns {
		option(&options)
	}
	burst := requestsPerMinute / 5 // 20% burst
	if burst < 1 {
		burst = 1
	}
	rl := &RateLimiter{
		limit:                      requestsPerMinute,
		burst:                      burst,
		authenticationAttemptLimit: options.authenticationAttemptLimit,
		stop:                       make(chan struct{}),
		proxies:                    newProxyTrust(workspaceEnv("gateway", "TRUSTED_PROXY_CIDRS")),
	}

	if redisURL := options.redisURL; redisURL != "" {
		rb, err := newRedisBackend(redisURL)
		if err != nil {
			log.Printf("WARNING: cannot connect to configured Redis: %v — falling back to in-memory rate limiter", err)
		} else {
			log.Printf("rate limiter using pooled Redis backend")
			rl.backend = rb
			return rl
		}
	}

	// In-memory fallback.
	mem := &memoryBackend{}
	rl.backend = mem
	go mem.cleanup(rl.stop)
	return rl
}

// Allow checks whether the given key is under the rate limit for the current
// minute window. Returns whether the request is allowed, how many requests
// remain, and the Unix timestamp when the window resets.
func (rl *RateLimiter) Allow(key string) (allowed bool, remaining int, resetUnix int64, err error) {
	return rl.allowWithBudget(key, rl.limit, rl.burst)
}

func (rl *RateLimiter) allowWithBudget(key string, limit, burst int) (allowed bool, remaining int, resetUnix int64, err error) {
	now := time.Now()
	minuteKey := fmt.Sprintf("rl:%s:%d", key, now.Unix()/60) // per-minute window
	resetAt := now.Truncate(time.Minute).Add(time.Minute)

	count, err := rl.backend.Increment(minuteKey, 120*time.Second) // 2 min TTL for cleanup
	if err != nil {
		return false, 0, resetAt.Unix(), err
	}

	effective := int64(limit + burst)
	if count > effective {
		return false, 0, resetAt.Unix(), nil
	}

	rem := int(effective - count)
	if rem < 0 {
		rem = 0
	}
	return true, rem, resetAt.Unix(), nil
}

type limiterFailureMode uint8

const (
	limiterFailOpen limiterFailureMode = iota
	limiterFailClosed
)

const authenticationAttemptLimitPerMinute = 10

// Middleware returns an http.Handler that enforces rate limiting. It reads the
// X-Org-Id header (set by the sidecar after auth) to determine the rate limit
// key. If no org ID is present, it falls back to the client IP.
//
// On 429 Too Many Requests it sets standard rate limit headers:
//
//	Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset
func (rl *RateLimiter) Middleware(mode limiterFailureMode, authenticationFactorAttempt bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Org-Id")
		keyKind := "org"
		limit := rl.limit
		burst := rl.burst
		if key == "" {
			key = rl.proxies.clientIP(r)
			keyKind = "ip"
		}
		if authenticationFactorAttempt {
			// Login MFA completion is public and has no canonical user/org
			// yet. Always bind the edge budget to the trusted client IP and
			// a dedicated namespace; the database independently enforces the
			// five-attempt transaction lock across every gateway replica.
			key = "auth-factor:" + rl.proxies.clientIP(r)
			keyKind = "authentication-factor-ip"
			limit = rl.authenticationAttemptLimit
			if limit <= 0 {
				limit = authenticationAttemptLimitPerMinute
			}
			burst = 0
		}

		allowed, remaining, resetUnix, err := rl.allowWithBudget(key, limit, burst)
		if err != nil {
			if mode == limiterFailOpen {
				log.Printf("rate limiter backend unavailable for availability route: %v", err)
				next.ServeHTTP(w, r)
				return
			}
			log.Printf("rate limiter backend unavailable for protected route: %v", err)
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "rate limit service unavailable")
			return
		}

		// Always set informational rate limit headers.
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))

		if !allowed {
			retryAfter := resetUnix - time.Now().Unix()
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			log.Printf("rate limit exceeded: key=%s (%s), limit=%d/min", key, keyKind, limit)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Stop terminates the background cleanup goroutine and closes the backend.
func (rl *RateLimiter) Stop() {
	close(rl.stop)
	if err := rl.backend.Close(); err != nil {
		log.Printf("rate limiter backend close error: %v", err)
	}
}

type proxyTrust struct {
	prefixes []netip.Prefix
}

func newProxyTrust(raw string) proxyTrust {
	var trust proxyTrust
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if addr, err := netip.ParseAddr(candidate); err == nil {
			addr = addr.Unmap()
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			trust.prefixes = append(trust.prefixes, netip.PrefixFrom(addr, bits))
			continue
		}
		if prefix, err := netip.ParsePrefix(candidate); err == nil {
			trust.prefixes = append(trust.prefixes, prefix.Masked())
		}
	}
	return trust
}

func (t proxyTrust) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (t proxyTrust) clientIP(r *http.Request) string {
	peer, ok := parsePeerAddress(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if !t.contains(peer) {
		return peer.String()
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 32 {
			return peer.String()
		}
		chain := make([]netip.Addr, 0, len(parts)+1)
		for _, part := range parts {
			addr, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil {
				return peer.String()
			}
			chain = append(chain, addr.Unmap())
		}
		chain = append(chain, peer)
		for index := len(chain) - 1; index >= 0; index-- {
			if index == 0 || !t.contains(chain[index]) {
				return chain[index].String()
			}
		}
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-Ip")); realIP != "" {
		if addr, err := netip.ParseAddr(realIP); err == nil {
			return addr.Unmap().String()
		}
	}
	return peer.String()
}

func parsePeerAddress(remoteAddr string) (netip.Addr, bool) {
	host := remoteAddr
	if splitHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = splitHost
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
