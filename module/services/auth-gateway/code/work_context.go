package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/workcontext"
)

// accountsJWKSPath is the standards-named endpoint accounts publishes its
// Ed25519 public keys on. It serves one document for both edge verification
// paths: the Work Context signing key is the access-token signing key.
const accountsJWKSPath = "/v1/auth/.well-known/jwks.json"

const workContextJWKSCacheTTL = 5 * time.Minute

// workContextVerifier inspects presented Work Contexts at the edge against the
// access-token JWKS published by accounts. The Work Context signing key is the
// access-token key, so the public gateway route serves both and the edge never
// needs Vault.
//
// It is core/workcontext's Inspector — the forwarding hop's entrypoint, and the
// one implementation of the capability's checks. The edge holds a trust root and
// a routing table and nothing more: it checks the encoding, the issuer, the
// signature under the key the capability names, the window, the shape, and the
// audience against the route THIS HOP resolved. It consults no live state and
// consumes nothing — the callee verifies at full strength and burns a single-use
// nonce exactly once.
//
// Every failure — a foreign encoding, a forged signature, an expired or widened
// context, an unknown key id, or an accounts outage that makes the key set
// unreachable — is classified as errInvalidWorkContext while retaining its
// underlying cause internally. The edge answers 401 without leaking which check
// failed or that a dependency is down.
//
// The cache carries no stale grace: a presented capability is an optional
// attenuation a caller may always retry, so this path fails closed the moment
// the key set expires. The access-token path makes the opposite call (see
// accessJWKS), because losing it would take authentication down entirely.
type workContextVerifier struct {
	cache *jwksCache[*workcontext.Inspector]
}

// workContextIssuer is the issuer accounts signs every Work Context as — the
// literal its permissions plugin is configured with (`WithWorkContextAuthority`
// in accounts' work.go). It is NOT the access-token `iss`, which follows the
// public origin; the capability's issuer is the authority's stable name.
const workContextIssuer = legacyTokenIssuer

// errInvalidWorkContext is the one error class this edge answers with.
var errInvalidWorkContext = errors.New("work context: invalid at this edge")

func newWorkContextVerifier(accountsBaseURL string) *workContextVerifier {
	cache := newJWKSCache(
		strings.TrimSuffix(accountsBaseURL, "/")+accountsJWKSPath,
		workContextJWKSCacheTTL,
		0,
		func(keys map[string]ed25519.PublicKey) (*workcontext.Inspector, error) {
			return &workcontext.Inspector{Issuer: workContextIssuer, Keys: keys}, nil
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

// Inspect checks a presented capability against the route it was presented TO,
// and returns what a hop may route on.
//
// `permitted` is the set of audiences the resolved route accepts. One entry is
// the ordinary case: `solution:<binding id>` for a solution route. A declared
// module route accepts two — the module's own identity and a viewer's delegated
// capability for it — and the capability passes when it is addressed to either.
//
// A catalog route passes NONE. Its destination is one of this host's own
// services, which resolves no audience of its own (the hop's name is never one),
// so there is no route target to hold the capability to: it is inspected for its
// encoding, issuer, signature, window and shape, and its audience is left to the
// callee that consumes it. That is exactly the strength this route had before the
// capability moved to core, and it is stated here rather than hidden behind an
// empty-string sentinel: the audience Inspect is given in that case is the
// capability's own, so the comparison is satisfied by construction, and that is
// the point of saying so.
func (v *workContextVerifier) Inspect(
	ctx context.Context, encoded string, permitted ...string,
) (*workcontext.Inspected, error) {
	decoded, err := workcontext.Decode(encoded)
	if err != nil {
		return nil, invalidWorkContext(err)
	}
	inspector, err := v.cache.resolve(ctx, decoded.Context().GetKeyId())
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(permitted))
	for _, audience := range permitted {
		if audience != "" {
			targets = append(targets, audience)
		}
	}
	if len(targets) == 0 {
		targets = []string{decoded.Context().GetAudience()}
	}
	var refusal error
	for _, target := range targets {
		inspected, err := inspector.Inspect(encoded, target)
		if err != nil {
			refusal = err
			continue
		}
		// Refused AFTER the signature check, deliberately: an unsigned token
		// claiming the hop's audience is an invalid token, not a routing
		// question, and answering the audience refusal first would tell an
		// unauthenticated caller which audience the hop answers to.
		if inspected.Audience() == gatewayAudience {
			return nil, fmt.Errorf("%w: a capability addressed to this forwarding hop is never consumed by it", errInvalidWorkContext)
		}
		return inspected, nil
	}
	// The refusal names neither the presented audience nor the permitted set: the
	// caller learns that the capability is not valid at this route, which is the
	// whole verdict, and not which consumers this route serves.
	return nil, invalidWorkContext(refusal)
}

// invalidWorkContext preserves the edge classification and the underlying cause.
// The HTTP boundary emits its static refusal without exposing either detail.
func invalidWorkContext(err error) error {
	return fmt.Errorf("%w: %w", errInvalidWorkContext, err)
}
