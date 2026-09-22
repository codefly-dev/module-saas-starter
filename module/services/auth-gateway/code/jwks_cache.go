package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	jwksRequestTimeout = 2 * time.Second
	jwksMaxBytes       = 256 * 1024
	jwksMaxKeys        = 64
	// jwksFailureBackoff is how long a failed fetch suppresses the next one on
	// the request path. Without it an unreachable publisher — or a stream of
	// tokens naming keys nobody publishes — would spend one round-trip per
	// request, adding the request timeout to every one of them.
	jwksFailureBackoff = time.Second
	// jwksProbeInterval is the minimum spacing between refetches triggered by a
	// key id the cached set does not list. It is a rate, deliberately not a
	// once-per-cache-window budget: a budget is consumed by whichever key id
	// asks first, so one token naming a key nobody publishes would block the
	// refetch that a genuinely rotated-in key needs, for the rest of the window.
	// A rate bounds the same fetch storm without letting one key id speak for
	// every other.
	jwksProbeInterval = 5 * time.Second
)

// errJWKSBackingOff is what the request path reports while a recent failure is
// still being backed off and no usable key set is held.
var errJWKSBackingOff = errors.New("JWKS refresh is backing off after a recent failure")

// jwksCache is the bounded, kid-keyed key cache behind both edge verification
// paths — presented Work Context capabilities and our own access tokens. It
// owns transport and freshness only: audience, claim, and revocation policy
// stay with each caller, so neither path inherits the other's trust rules.
//
// Untrusted token input decides which key id is looked up, so every path out of
// here is bounded: at most one fetch in flight (concurrent cold requests share
// it), at most one refetch per jwksProbeInterval for an unrecognised key id, a
// request timeout, a response size cap, and a key-count cap. Redirects are not
// followed — the configured origin is the only origin.
//
// T is whatever a caller builds from a fetched key set (a key map, or a
// verifier constructed over one). It is built once per fetch, not per request.
type jwksCache[T any] struct {
	url        string
	httpClient *http.Client
	cacheTTL   time.Duration
	// staleGrace is how long past cacheTTL a previously fetched key set may
	// still answer while the publisher is unreachable. Zero fails closed the
	// moment the cache expires.
	staleGrace time.Duration
	timeout    time.Duration
	now        func() time.Time
	// build derives the cached artifact from a freshly parsed key set.
	build func(map[string]ed25519.PublicKey) (T, error)
	// wrap folds a transport, parse, or build failure into the caller's error
	// class, so callers never have to reason about this package's errors.
	wrap func(error) error
	// observe records the outcome of each completed fetch.
	observe func(error)

	mu      sync.Mutex
	current *jwksEntry[T]
	// retryAfter suppresses request-path fetches for a short window after one
	// fails. refresh (the background warm loop) is not subject to it: that loop
	// paces its own retries and is what makes recovery prompt.
	retryAfter time.Time
	// nextProbeAt rate-limits refetches for key ids the cached set does not
	// list, so a stream of garbage tokens carrying attacker-chosen key ids
	// cannot turn verification into an unbounded fetch loop. It is only ever set
	// by an actual probe, so a key rotated in while the cache is fresh is
	// discovered by the first token that names it.
	nextProbeAt time.Time
	inflight    *jwksFetch[T]
}

// jwksEntry is one immutable fetched key set and the artifact built over it.
type jwksEntry[T any] struct {
	value     T
	keyIDs    map[string]struct{}
	expiresAt time.Time
}

// jwksFetch is one shared, in-flight fetch. Waiters block on done and then read
// entry/err, which the leader publishes before closing done.
type jwksFetch[T any] struct {
	done  chan struct{}
	entry *jwksEntry[T]
	err   error
}

// newJWKSCache builds a cache over url. The caller supplies the artifact
// builder and its error class; everything else is the shared bounded policy.
func newJWKSCache[T any](
	url string,
	cacheTTL, staleGrace time.Duration,
	build func(map[string]ed25519.PublicKey) (T, error),
	wrap func(error) error,
) *jwksCache[T] {
	return &jwksCache[T]{
		url:        url,
		cacheTTL:   cacheTTL,
		staleGrace: staleGrace,
		timeout:    jwksRequestTimeout,
		now:        time.Now,
		build:      build,
		wrap:       wrap,
		httpClient: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// refresh installs the current key set. Callers use it to warm the cache at
// boot and to keep it warm.
func (c *jwksCache[T]) refresh(ctx context.Context) error {
	_, err := c.coalescedFetch(ctx)
	return err
}

// resolve returns the artifact that should answer for keyID. A fresh cache that
// already lists the key answers with no I/O; an unrecognised key id spends the
// window's single probe; a cold or expired cache refreshes.
//
// When the refresh fails, the last good key set still answers until it is
// staleGrace past its expiry — the deliberate availability half of the
// tradeoff, bounded so a key withdrawn by the publisher stops verifying within
// cacheTTL + staleGrace even if the publisher never becomes reachable again.
// With no cached key set at all there is nothing to serve and the error stands.
// A failed fetch also suppresses the next request-path fetch for
// jwksFailureBackoff, so an outage costs one round-trip per window rather than
// one per request.
func (c *jwksCache[T]) resolve(ctx context.Context, keyID string) (T, error) {
	var zero T

	c.mu.Lock()
	now := c.now()
	cached := c.current
	if cached != nil && now.Before(cached.expiresAt) {
		if _, known := cached.keyIDs[keyID]; known {
			c.mu.Unlock()
			return cached.value, nil
		}
		// Fresh cache, unrecognised key id. Refetch in case the publisher has
		// rotated a key in since this set was fetched, but no more often than
		// jwksProbeInterval — that spacing is what bounds the fetch rate, and
		// unlike a per-window budget it cannot be exhausted on one key id and
		// then denied to another.
		if now.Before(c.nextProbeAt) {
			c.mu.Unlock()
			return cached.value, nil
		}
		c.nextProbeAt = now.Add(jwksProbeInterval)
		c.mu.Unlock()

		entry, err := c.coalescedFetch(ctx)
		if err != nil {
			// The probe told us nothing; the cached set is still fresh and is
			// what the caller must decide against.
			return cached.value, nil
		}
		return entry.value, nil
	}
	if now.Before(c.retryAfter) {
		usable := cached != nil && !now.After(cached.expiresAt.Add(c.staleGrace))
		c.mu.Unlock()
		if usable {
			return cached.value, nil
		}
		return zero, c.wrap(errJWKSBackingOff)
	}
	c.mu.Unlock()

	entry, err := c.coalescedFetch(ctx)
	if err == nil {
		return entry.value, nil
	}
	if fallback, ok := c.withinStaleGrace(); ok {
		return fallback.value, nil
	}
	return zero, err
}

// everLoaded reports whether a key set has ever been fetched. It is what
// readiness is built on, deliberately in preference to "the cached set is still
// fresh": a gateway that has never acquired keys cannot serve authenticated
// traffic at all, while one holding a stale set is degraded but still routing —
// including the public routes that never needed a key. It does no I/O.
func (c *jwksCache[T]) everLoaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current != nil
}

func (c *jwksCache[T]) withinStaleGrace() (*jwksEntry[T], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.now().After(c.current.expiresAt.Add(c.staleGrace)) {
		return nil, false
	}
	return c.current, true
}

// coalescedFetch performs one fetch shared across concurrent callers and
// installs the result. The network round-trip runs without holding mu; the
// cache swap and the inflight hand-off happen under one locked section, so a
// caller arriving after the leader finishes sees the fresh cache rather than
// starting a second fetch.
func (c *jwksCache[T]) coalescedFetch(ctx context.Context) (*jwksEntry[T], error) {
	c.mu.Lock()
	if call := c.inflight; call != nil {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.entry, call.err
		case <-ctx.Done():
			return nil, c.wrap(ctx.Err())
		}
	}
	call := &jwksFetch[T]{done: make(chan struct{})}
	c.inflight = call
	c.mu.Unlock()

	entry, err := c.fetchEntry(ctx)
	if c.observe != nil {
		c.observe(err)
	}

	c.mu.Lock()
	c.inflight = nil
	if err == nil {
		entry.expiresAt = c.now().Add(c.cacheTTL)
		c.current = entry
		c.retryAfter = time.Time{}
	} else {
		c.retryAfter = c.now().Add(jwksFailureBackoff)
	}
	c.mu.Unlock()

	call.entry, call.err = entry, err
	close(call.done)
	return entry, err
}

func (c *jwksCache[T]) fetchEntry(ctx context.Context) (*jwksEntry[T], error) {
	keys, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	value, err := c.build(keys)
	if err != nil {
		return nil, c.wrap(err)
	}
	keyIDs := make(map[string]struct{}, len(keys))
	for keyID := range keys {
		keyIDs[keyID] = struct{}{}
	}
	return &jwksEntry[T]{value: value, keyIDs: keyIDs}, nil
}

func (c *jwksCache[T]) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, c.wrap(err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, c.wrap(fmt.Errorf("JWKS returned HTTP %d", response.StatusCode))
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, jwksMaxBytes+1))
	if err != nil {
		return nil, c.wrap(err)
	}
	if len(payload) > jwksMaxBytes {
		return nil, c.wrap(fmt.Errorf("JWKS exceeds %d bytes", jwksMaxBytes))
	}
	keys, err := parseJWKS(payload)
	if err != nil {
		return nil, c.wrap(err)
	}
	return keys, nil
}

// parseJWKS decodes a JWKS into kid → Ed25519 public key. Keys of another type,
// curve, algorithm, or use are ignored rather than rejected — a publisher may
// legitimately serve keys this edge has no use for — but anything that claims
// to be an Ed25519 signing key and is not one rejects the whole document, so a
// malformed or ambiguous key set never half-loads.
func parseJWKS(payload []byte) (map[string]ed25519.PublicKey, error) {
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, err
	}
	if len(document.Keys) == 0 || len(document.Keys) > jwksMaxKeys {
		return nil, fmt.Errorf("JWKS must contain between 1 and %d keys", jwksMaxKeys)
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" ||
			(key.Alg != "" && key.Alg != "EdDSA") ||
			(key.Use != "" && key.Use != "sig") {
			continue
		}
		if key.Kid == "" {
			return nil, fmt.Errorf("JWKS key is missing a key id")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("JWKS key %q is not a valid Ed25519 key", key.Kid)
		}
		if _, duplicate := keys[key.Kid]; duplicate {
			return nil, fmt.Errorf("JWKS has duplicate key id %q", key.Kid)
		}
		keys[key.Kid] = decoded
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS has no Ed25519 signing key")
	}
	return keys, nil
}
