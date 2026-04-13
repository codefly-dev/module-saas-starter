package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

// redisBackend uses a single persistent TCP connection speaking the RESP
// protocol. It avoids pulling in a third-party Redis client to keep the
// dependency footprint minimal. Safe for concurrent use via a mutex around
// the connection.
type redisBackend struct {
	mu   sync.Mutex
	conn net.Conn
	rd   *bufio.Reader
}

func newRedisBackend(addr string) (*redisBackend, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("redis connect %s: %w", addr, err)
	}
	return &redisBackend{conn: conn, rd: bufio.NewReader(conn)}, nil
}

// Increment runs INCR key; if the result is 1 (new key) it also sends
// EXPIRE key <ttl-seconds> to auto-clean. This is the standard Redis
// rate-limit pattern.
func (r *redisBackend) Increment(key string, ttl time.Duration) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ttlSec := int(ttl.Seconds())
	if ttlSec < 1 {
		ttlSec = 1
	}

	// Pipeline: INCR + EXPIRE in one write to save a round trip.
	cmd := respArray("INCR", key) + respArray("EXPIRE", key, strconv.Itoa(ttlSec))
	if _, err := r.conn.Write([]byte(cmd)); err != nil {
		return 0, fmt.Errorf("redis write: %w", err)
	}

	// Read INCR reply.
	count, err := readRESPInteger(r.rd)
	if err != nil {
		return 0, fmt.Errorf("redis read INCR: %w", err)
	}

	// Read EXPIRE reply (we don't need its value).
	if _, err := readRESPInteger(r.rd); err != nil {
		return 0, fmt.Errorf("redis read EXPIRE: %w", err)
	}

	return count, nil
}

func (r *redisBackend) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.Close()
}

// respArray builds a RESP array command, e.g. "*2\r\n$4\r\nINCR\r\n$3\r\nfoo\r\n".
func respArray(args ...string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("*%d\r\n", len(args)))
	for _, a := range args {
		b.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(a), a))
	}
	return b.String()
}

// readRESPInteger reads a RESP integer reply (":123\r\n") or a simple
// integer from an error/status line.
func readRESPInteger(rd *bufio.Reader) (int64, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return 0, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return 0, fmt.Errorf("empty RESP line")
	}
	switch line[0] {
	case ':': // integer
		return strconv.ParseInt(line[1:], 10, 64)
	case '-': // error
		return 0, fmt.Errorf("redis error: %s", line[1:])
	default:
		return 0, fmt.Errorf("unexpected RESP type %q", line)
	}
}

// -------------------------------------------------------------------------
// RateLimiter (backend-agnostic)
// -------------------------------------------------------------------------

// RateLimiter implements per-key fixed-window rate limiting.
//
// If REDIS_URL is set, it uses Redis (INCR + EXPIRE) for multi-instance
// consistency. Otherwise it falls back to an in-memory backend that is
// fine for local dev where only one process runs.
type RateLimiter struct {
	backend rateLimitBackend
	limit   int // requests per minute
	burst   int // burst allowance above limit
	stop    chan struct{}
}

// NewRateLimiter creates a rate limiter allowing requestsPerMinute requests
// per key per minute, with a 20% burst allowance. If REDIS_URL is set it
// connects to Redis; otherwise it uses in-memory counters with a background
// cleanup goroutine.
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

	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		addr := redisURL
		// Strip redis:// scheme if present.
		addr = strings.TrimPrefix(addr, "redis://")
		addr = strings.TrimPrefix(addr, "rediss://")
		// Remove any path component (e.g. /0).
		if idx := strings.Index(addr, "/"); idx != -1 {
			addr = addr[:idx]
		}
		rb, err := newRedisBackend(addr)
		if err != nil {
			log.Printf("WARNING: cannot connect to Redis at %s: %v — falling back to in-memory rate limiter", addr, err)
		} else {
			log.Printf("rate limiter using Redis backend at %s", addr)
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
func (rl *RateLimiter) Allow(key string) (allowed bool, remaining int, resetUnix int64) {
	now := time.Now()
	minuteKey := fmt.Sprintf("rl:%s:%d", key, now.Unix()/60) // per-minute window
	resetAt := now.Truncate(time.Minute).Add(time.Minute)

	count, err := rl.backend.Increment(minuteKey, 120*time.Second) // 2 min TTL for cleanup
	if err != nil {
		// On backend failure, allow the request (fail open) and log.
		log.Printf("rate limiter backend error: %v (allowing request)", err)
		return true, rl.limit, resetAt.Unix()
	}

	effective := int64(rl.limit + rl.burst)
	if count > effective {
		return false, 0, resetAt.Unix()
	}

	rem := int(effective - count)
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

// Stop terminates the background cleanup goroutine and closes the backend.
func (rl *RateLimiter) Stop() {
	close(rl.stop)
	if err := rl.backend.Close(); err != nil {
		log.Printf("rate limiter backend close error: %v", err)
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

