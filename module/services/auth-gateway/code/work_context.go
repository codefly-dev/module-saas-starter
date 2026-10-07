package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/sdk-go/workcontext"
)

// accountsJWKSPath is the standards-named endpoint accounts publishes its
// Ed25519 public keys on. It serves one document for both edge verification
// paths: the Work Context signing key is the access-token signing key.
const accountsJWKSPath = "/v1/auth/.well-known/jwks.json"

const workContextJWKSCacheTTL = 5 * time.Minute

// workContextVerifier verifies presented Work Contexts at the edge against the
// access-token JWKS published by accounts. The Work Context signing key is the
// access-token key, so the public gateway route serves both and the edge never
// needs Vault.
//
// Every failure — a forged signature, an expired or widened context, an unknown
// key id, or an accounts outage that makes the key set unreachable — collapses
// to workcontext.ErrWorkContextInvalid. The edge answers 401 without leaking which
// check failed or that a dependency is down.
//
// The cache carries no stale grace: a presented capability is an optional
// attenuation a caller may always retry, so this path fails closed the moment
// the key set expires. The access-token path makes the opposite call (see
// accessJWKS), because losing it would take authentication down entirely.
type workContextVerifier struct {
	cache *jwksCache[*workcontext.WorkContextVerifier]
}

func newWorkContextVerifier(accountsBaseURL string) *workContextVerifier {
	cache := newJWKSCache(
		strings.TrimSuffix(accountsBaseURL, "/")+accountsJWKSPath,
		workContextJWKSCacheTTL,
		0,
		func(keys map[string]ed25519.PublicKey) (*workcontext.WorkContextVerifier, error) {
			return workcontext.NewWorkContextVerifier(workcontext.WorkContextVerifierOptions{PublicKeys: keys})
		},
		invalidWorkContext,
	)
	cache.observe = func(err error) { recordJWKSRefresh(jwksKeySetWorkContext, err) }
	return &workContextVerifier{cache: cache}
}

// Refresh warms the cached key set. A callee calls it at boot so verification
// fails closed from the first request rather than racing the first fetch.
func (v *workContextVerifier) Refresh(ctx context.Context) error {
	return v.cache.refresh(ctx)
}

// gatewayAudience is the name a capability may NEVER be addressed to.
//
// This gateway is a forwarding hop: it inspects a presented capability and never
// consumes one. A token addressed to the hop itself is therefore a token nobody
// will ever consume — and worse, it is what a caller would mint to get the hop to
// treat a capability as its own rather than as something to forward. There is no
// legitimate producer of such a token, so it is refused by name rather than
// forwarded and left for a callee to puzzle over.
const gatewayAudience = "auth-gateway"

// Verify establishes trust for a presented Work Context against the route it was
// presented TO.
//
// `expected` is the audience derived from the resolved route — `solution:<binding
// id>` for a solution route, the module capability audience for a declared module
// route, and EMPTY for a catalog route, whose destination is this host's own
// service and therefore has no audience of its own (see gatewayAudience: the hop's
// name is never one).
//
// An empty `expected` means "do not compare", and that is sdk-go's own sentinel
// (`check.want != ""` in workcontext/work_context.go), not a choice made here. It
// is load-bearing that this is the ONLY place that relies on it and that the
// reliance is explicit: for the two surfaces where an audience IS derivable, a
// value is always passed, so a capability minted for one solution cannot be spent
// on another's route.
//
// What is checked here is the signature, the window and the audience. Scope and
// consumption stay the callee's: this hop never consumes a nonce, never reads
// seals, and never grants.
func (v *workContextVerifier) Verify(
	ctx context.Context, token workcontext.WorkContextToken, expected string,
) error {
	keyID, err := workContextTokenKeyID(token)
	if err != nil {
		return err
	}
	verifier, err := v.cache.resolve(ctx, keyID)
	if err != nil {
		return err
	}
	claims, err := verifier.Verify(token, workcontext.WorkContextExpectations{Audience: expected})
	if err != nil {
		return invalidWorkContext(err)
	}
	// Refused AFTER the signature check, deliberately: an unsigned token claiming
	// the hop's audience is an invalid token, not a routing question, and answering
	// the audience refusal first would tell an unauthenticated caller which
	// audience the hop answers to.
	if claims.GetAudience() == gatewayAudience {
		return fmt.Errorf("%w: a capability addressed to this forwarding hop is never consumed by it", workcontext.ErrWorkContextInvalid)
	}
	return nil
}

func workContextTokenKeyID(token workcontext.WorkContextToken) (string, error) {
	segment, _, found := strings.Cut(token.Encoded(), ".")
	if !found {
		return "", fmt.Errorf("%w: malformed token", workcontext.ErrWorkContextInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return "", invalidWorkContext(err)
	}
	probe := struct {
		KeyID string `json:"key_id"`
	}{}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return "", invalidWorkContext(err)
	}
	if probe.KeyID == "" {
		return "", fmt.Errorf("%w: token is missing a key id", workcontext.ErrWorkContextInvalid)
	}
	return probe.KeyID, nil
}

// invalidWorkContext folds an arbitrary underlying failure into the single
// invalid sentinel, so callers see one error class regardless of cause.
func invalidWorkContext(err error) error {
	return fmt.Errorf("%w: %v", workcontext.ErrWorkContextInvalid, err)
}
