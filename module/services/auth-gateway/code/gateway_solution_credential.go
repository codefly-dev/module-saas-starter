package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// --- Per-solution cryptographic identity ---
//
// Solution registration is bound to a publisher the same way module federation
// is (see gateway_modules.go), and for a strictly stronger reason. A module
// registration decides where /v1/<prefix>/* traffic goes; a solution
// registration ALSO decides which script the frontend loads as a
// Module-Federation remote — code that then runs in the host origin with the
// viewer's access token. So the two halves, gateway and frontend, accept the
// SAME credential: one owner-bound authority contract, verified independently on
// each side, rather than a shared secret that says nothing about who is calling.

// solutionRegisterSuffix and solutionRegistrationTokenSuffix are the reserved
// sub-paths of /solutions/. Neither is a possible solution id: validCatalogIdentity
// rejects the leading underscore, so no registrant can shadow them.
const solutionRegistrationTokenSegment = "_registration-token"

// solutionServicePrefix marks the RouteEntry.Service of a registered solution
// route, so proxyTo can tell a runtime-registered upstream from a catalog one.
const solutionServicePrefix = "solution:"

// solutionRegistrationHeader carries the signed per-solution registration token.
const solutionRegistrationHeader = "X-Codefly-Solution-Registration"

// solutionSecretHeader carries the solution's own registration secret. The
// gateway consumes it in the exchange and forwards it only on the internal leg,
// to accounts, which holds the digest to compare it against.
const solutionSecretHeader = "X-Codefly-Solution-Secret"

// solutionRegistrationAudience scopes a solution-registration token to this
// single purpose. Access tokens carry aud "saas-starter" and module-registration
// tokens carry "module-registration"; even though all three may be signed with
// the same key, the audience check makes them non-interchangeable — an access
// token cannot register a solution, and a module credential cannot reach the
// authority to publish host-origin code.
const solutionRegistrationAudience = "solution-registration"

// solutionRegisterMaxBytes bounds a registration body. The payload is two short
// strings; anything larger is a malformed or hostile caller, and reading it is
// work an unauthenticated-at-parse-time endpoint should not do.
const solutionRegisterMaxBytes = 4096

// mintSolutionRegistrationMethod is accounts' solution-credential exchange. Like
// its module counterpart it is EXPOSURE_INTERNAL, so the generated mesh policy
// admits this gateway's service account to exactly this path.
const mintSolutionRegistrationMethod = "/saas.accounts.v1.ModuleCapabilitiesService/MintSolutionRegistration"

// solutionRegistrationClaims is the signed assertion a solution presents. `sub`
// is the publisher identity that owns the registration; Solution is the single
// catalog-identity segment that identity may register, update, or delete.
type solutionRegistrationClaims struct {
	jwt.RegisteredClaims
	Solution string `json:"solution"`
}

// verifySolutionRegistration parses and validates a solution registration token
// with the same alg-locked Ed25519 discipline as an access token — same
// published key set selected by the token's kid, plus issuer, expiry, and this
// solution-registration audience. It fails closed: a nil check, an unreachable
// or unrecognised key, an empty or malformed token, a wrong or absent audience,
// a missing jti, or an expired token all yield ok=false.
func (s *ExtAuthz) verifySolutionRegistration(ctx context.Context, tokenString string) (*solutionRegistrationClaims, bool) {
	if s == nil || s.keys == nil || tokenString == "" {
		return nil, false
	}
	claims := &solutionRegistrationClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(solutionRegistrationAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(tokenClockSkewLeeway),
	)
	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "EdDSA" {
			return nil, fmt.Errorf("alg forbidden: %s", t.Method.Alg())
		}
		keyID, _ := t.Header["kid"].(string)
		return s.keys.keyFor(ctx, keyID)
	})
	if err != nil || !token.Valid {
		return nil, false
	}
	// Single-use enforcement keys on the jti, so a token without one is not
	// replay-protectable and is refused rather than admitted unprotected.
	if claims.ID == "" || claims.ExpiresAt == nil || claims.Subject == "" {
		return nil, false
	}
	return claims, true
}

// mintSolutionRegistration runs the internal leg over the existing accounts
// connection, presenting the gateway's own cluster-internal credential.
func (s *ExtAuthz) mintSolutionRegistration(
	ctx context.Context, solutionID, secret string,
) (*accountsv1.SolutionMintRegistrationResponse, error) {
	if s.backendConn == nil {
		return nil, fmt.Errorf("accounts connection not configured")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	response := &accountsv1.SolutionMintRegistrationResponse{}
	request := &accountsv1.SolutionMintRegistrationRequest{SolutionId: solutionID, Secret: secret}
	if err := s.backendConn.Invoke(ctx, mintSolutionRegistrationMethod, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// --- Registration replay protection ---

// registrationReplayGuard makes each registration credential single-use. A token
// is minted per attempt and lives five minutes, so the set it has to remember is
// small and self-clearing: an entry is dropped once the token it names could no
// longer verify anyway.
type registrationReplayGuard struct {
	mu   sync.Mutex
	used map[string]time.Time
	now  func() time.Time
}

func newRegistrationReplayGuard() *registrationReplayGuard {
	return &registrationReplayGuard{used: map[string]time.Time{}, now: time.Now}
}

// consume records a first use of jti and reports true. A second call for the
// same jti reports false while the original token could still verify.
//
// Expired entries are swept on each call rather than by a timer: the sweep is
// bounded by how many credentials were issued in one token lifetime, and a guard
// nobody calls holds nothing worth reclaiming.
func (g *registrationReplayGuard) consume(jti string, expiresAt time.Time) bool {
	if jti == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for seen, expiry := range g.used {
		// The verifier admits a token up to tokenClockSkewLeeway past exp, so an
		// entry has to outlive its token by the same margin to stay useful.
		if now.After(expiry.Add(tokenClockSkewLeeway)) {
			delete(g.used, seen)
		}
	}
	if _, replayed := g.used[jti]; replayed {
		return false
	}
	g.used[jti] = expiresAt
	return true
}

// handleSolutionRegistrationToken serves POST /solutions/_registration-token,
// the exchange a solution runs immediately before registering: it presents the
// registration secret its composition gave it and receives the signed,
// solution-bound token both this surface and the frontend require.
//
// It is the exact counterpart of the module handshake, and brokers for the same
// reason: the gateway holds only the public half of the signing key, and a
// solution cannot reach accounts' internal listener itself.
func (g *Gateway) handleSolutionRegistrationToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Perimeter check, as on the module exchange: the solution secret alone
	// identifies the solution, but this listener does not rate-limit unrouted
	// paths, so guessing one must first cost an attacker the shared credential.
	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	secret := r.Header.Get(solutionSecretHeader)
	if secret == "" {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, solutionRegisterMaxBytes)).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !validCatalogIdentity(payload.ID) {
		httpError(w, http.StatusBadRequest, "invalid id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), moduleRegistrationExchangeTimeout)
	defer cancel()
	issued, err := g.authz.mintSolutionRegistration(ctx, payload.ID, secret)
	if err != nil {
		// accounts answers undeclared-solution and wrong-secret identically, so
		// relaying its refusal reveals nothing about what a composition declared.
		if status.Code(err) == codes.PermissionDenied {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		httpError(w, http.StatusBadGateway, "registration token unavailable")
		return
	}
	body, err := json.Marshal(map[string]string{
		"token":     issued.GetToken(),
		"expiresAt": issued.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, "registration token unavailable")
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// authorizeSolutionRegistration verifies the presented registration credential
// and burns its single use. It writes the refusal itself and reports ok=false,
// so both mutations share one authentication path.
func (g *Gateway) authorizeSolutionRegistration(w http.ResponseWriter, r *http.Request) (*solutionRegistrationClaims, bool) {
	claims, ok := g.authz.verifySolutionRegistration(r.Context(), r.Header.Get(solutionRegistrationHeader))
	if !ok {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	// A registration credential is fetched per attempt and presented once. Burn
	// its jti so a copy captured in transit or in a log cannot be replayed inside
	// its remaining lifetime to re-point a route after the legitimate registrant
	// has moved on.
	if !g.registrationReplay.consume(claims.ID, claims.ExpiresAt.Time) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return claims, true
}
