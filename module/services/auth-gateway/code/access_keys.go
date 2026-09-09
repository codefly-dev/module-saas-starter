package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	// accessJWKSCacheTTL bounds how long a published key set answers before the
	// gateway re-reads it, and so how long after accounts publishes a new key a
	// gateway can still reject tokens signed by it: at worst one TTL, and in
	// practice the first token carrying the unrecognised key id spends the
	// window's probe and picks it up immediately.
	accessJWKSCacheTTL = 5 * time.Minute
	// accessJWKSStaleGrace is the availability half of the tradeoff. While
	// accounts is unreachable the last good key set keeps verifying, so a
	// restart or a rollout on the accounts side does not take authentication
	// down with it. It also bounds retirement: a key accounts stops publishing
	// stops verifying here within accessJWKSCacheTTL + accessJWKSStaleGrace,
	// even if accounts never becomes reachable again.
	accessJWKSStaleGrace = 10 * time.Minute
)

var (
	// errNoVerificationKeys means the gateway holds no usable key set at all —
	// a configuration/availability failure, answered 503, never 401. It is the
	// recoverable state: the next request or the background warm loop retries.
	errNoVerificationKeys = errors.New("access-token verification keys unavailable")
	// errUnknownKeyID means the presented key id is not in the published set.
	// Unlike the above this is a bad credential, answered 401.
	errUnknownKeyID = errors.New("access token names an unknown key id")
	// errAmbiguousKeyID means the token carries no key id while the publisher
	// advertises more than one — see accessKeyFor.
	errAmbiguousKeyID = errors.New("access token has no key id and the key set is ambiguous")
)

// accessKeys supplies the Ed25519 public key an access token must verify
// against. main wires the JWKS-backed implementation; keeping it an interface
// lets the readiness probe ask whether verification is configured at all
// without reaching for the network.
type accessKeys interface {
	// keyFor returns the verification key for keyID, which is the token's `kid`
	// header and may be empty.
	keyFor(ctx context.Context, keyID string) (ed25519.PublicKey, error)
	// usable reports whether a key set is currently held. It does no I/O.
	usable() bool
}

// accessJWKS resolves access-token verification keys from the JWKS accounts
// publishes, keyed by `kid`. accounts serves its current signing key alongside
// every retained verification key, so during a rotation overlap this gateway
// accepts tokens signed by either without a restart, and a gateway that starts
// mid-overlap accepts both regardless of the order the document lists them in.
//
// The origin comes from Codefly runtime service discovery, never from anything
// a token carries.
type accessJWKS struct {
	cache *jwksCache[map[string]ed25519.PublicKey]
}

func newAccessJWKS(accountsBaseURL string) *accessJWKS {
	cache := newJWKSCache(
		strings.TrimSuffix(accountsBaseURL, "/")+accountsJWKSPath,
		accessJWKSCacheTTL,
		accessJWKSStaleGrace,
		func(keys map[string]ed25519.PublicKey) (map[string]ed25519.PublicKey, error) { return keys, nil },
		func(err error) error { return fmt.Errorf("%w: %v", errNoVerificationKeys, err) },
	)
	cache.observe = func(err error) { recordJWKSRefresh(jwksKeySetAccessToken, err) }
	return &accessJWKS{cache: cache}
}

func (a *accessJWKS) keyFor(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	keys, err := a.cache.resolve(ctx, keyID)
	if err != nil {
		return nil, err
	}
	return accessKeyFor(keys, keyID)
}

func (a *accessJWKS) usable() bool {
	_, ok := a.cache.snapshot()
	return ok
}

// refresh reloads the published key set.
func (a *accessJWKS) refresh(ctx context.Context) error {
	return a.cache.refresh(ctx)
}

// accessKeyFor picks the verification key for a presented key id.
//
// An unrecognised key id selects nothing: verification fails rather than
// falling back to some other published key, so a token signed by a key this
// deployment does not trust can never be verified by one it does.
//
// A token carrying no `kid` at all is accepted only when the publisher
// advertises exactly one key, where there is nothing to choose between. This is
// the compatibility policy for pre-`kid` tokens: accounts has stamped `kid` on
// every access token it mints since the per-key rotation work, and access
// tokens live three minutes, so the only deployment where a kid-less token can
// still appear is one that has never rotated — which is exactly the
// single-key case. During an overlap the same token is rejected, because
// picking one of several keys for it would be arbitrary.
func accessKeyFor(keys map[string]ed25519.PublicKey, keyID string) (ed25519.PublicKey, error) {
	if keyID == "" {
		if len(keys) != 1 {
			return nil, errAmbiguousKeyID
		}
		for _, key := range keys {
			return key, nil
		}
	}
	key, known := keys[keyID]
	if !known {
		return nil, errUnknownKeyID
	}
	return key, nil
}

// keepAccessKeysWarm holds the access-token key set current for the life of the
// process. It is what makes a failed initial acquisition recoverable: accounts
// being unreachable at boot leaves the gateway not-ready rather than
// permanently unable to verify JWTs, and authentication starts working on its
// own as soon as accounts answers. Per-request resolution refreshes lazily too,
// so this loop only removes the first-request latency and keeps readiness
// honest while no traffic is arriving.
func keepAccessKeysWarm(ctx context.Context, keys *accessJWKS) {
	// The retry ceiling is low on purpose: while no key set is held the gateway
	// reports not-ready and receives no traffic, so nothing else will trigger a
	// lazy refresh — this loop alone decides how quickly it comes back after
	// accounts recovers.
	const (
		minBackoff = 500 * time.Millisecond
		maxBackoff = 5 * time.Second
	)
	backoff := minBackoff
	// Log each change of state once rather than every attempt, so a long
	// accounts outage leaves a single warning instead of one per backoff tick.
	const (
		unknown = iota
		failing
		loaded
	)
	state := unknown
	for {
		err := keys.refresh(ctx)
		switch {
		case err != nil && state != failing:
			log.Printf("WARNING: access-token JWKS unavailable: %v (a cached key set still verifies for up to %s)", err, accessJWKSStaleGrace)
			state = failing
		case err == nil && state != loaded:
			log.Printf("access-token verification keys loaded from the published JWKS")
			state = loaded
		}
		wait := accessJWKSCacheTTL / 2
		if err != nil {
			wait = backoff
			if backoff < maxBackoff {
				backoff *= 2
			}
		} else {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
