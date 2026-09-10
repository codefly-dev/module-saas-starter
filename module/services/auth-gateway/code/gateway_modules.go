package main

// Composed-module REST federation.
//
// A composition can add modules and solutions that expose their own REST
// endpoints under /v1/<module>/*. Rather than redeploying this gateway with a
// new generated catalog every time, a composition-local module self-registers
// its upstream at startup (POST /modules/_register) and the gateway then proxies
// /v1/<module>/* to it — but ONLY after the generated catalog has no match, so a
// registered prefix can never shadow or override a catalog route.
//
// Registration only adds a proxy target; it never relaxes authentication. Every
// proxied /v1/<module>/* request runs the same ext_authz Check and identity
// projection as a protected catalog route, so a bearer-less call is denied at
// the gateway (401) regardless of what is registered.
//
// Trust model: registration is gated on a per-module cryptographic identity, not
// the shared cluster-internal token. The caller presents a signed registration
// token (X-Codefly-Module-Registration) that binds a module identity (its `sub`)
// to exactly one prefix; the gateway verifies it with the same alg-locked Ed25519
// discipline as an access token (audience-locked to module registration, so an
// access token can never be replayed as a registration credential and vice
// versa). A module holding a token for "documents" therefore cannot register
// "billing" — the shared secret gave no such per-caller binding. The enforceable
// guardrails below are: authenticated AND prefix-bound (signed token), well-formed
// single-segment prefix, first-claim-wins (a prefix already held by a different
// upstream cannot be taken over), never shadowing the catalog, and a
// composition-local (mesh) upstream only.
//
// That token comes from accounts, the authority whose key the gateway already
// trusts through JWKS, and a module obtains one by exchanging the registration
// secret its composition provisioned (POST /modules/_registration-token below).
// The gateway cannot mint — it holds only the public half. What that buys is
// narrower than "separation of authority": a gateway compromise routes traffic
// anywhere regardless of who signs. It buys that the prefix→module binding is
// declared once, in configuration accounts reads, so no code path on the request
// side — here or in a module — can widen who may claim a prefix.
//
// The token binds the prefix (a module's stable identity), not the upstream URL,
// which is chosen at runtime (loopback in dev, cluster DNS in prod) and would be
// brittle to pin. The upstream is instead constrained by the mesh-host guard at
// register time and — because a registrant is no longer trusted to name mesh
// hosts that later resolve off-mesh — by a resolve-time address check at proxy
// dial time (isAllowedResolvedModuleIP), which closes the DNS-rebinding window a
// name-only check leaves open.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const moduleRegisterPath = "/modules/_register"

// handleModuleRegister serves POST /modules/_register. It returns true when it
// has handled the request (the caller must then return). Any other path is left
// for the normal routing path.
func (g *Gateway) handleModuleRegister(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != moduleRegisterPath {
		return false
	}
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}

	// Guardrail 1 — per-module cryptographic identity. Registration decides where
	// authenticated module traffic (bearer + injected identity) is forwarded, so
	// the caller must prove — with a signed, per-module registration token rather
	// than the shared cluster-internal secret — that it owns the prefix it claims.
	// verifyModuleRegistration fails closed on a missing/unset key, bad signature,
	// wrong audience, or expiry, so an unauthenticated edge caller can never point
	// a prefix at an attacker-controlled upstream and harvest forwarded bearers.
	// The prefix→identity binding itself is enforced below, once the payload prefix
	// is known.
	claims, ok := g.authz.verifyModuleRegistration(r.Context(), r.Header.Get(moduleRegistrationHeader))
	if !ok {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	var payload struct {
		Prefix   string `json:"prefix"`
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return true
	}

	// Guardrail 2 (part) — identity-bound. Under a shared token the strongest
	// well-formedness we can require is a single valid identity segment: no
	// slashes, no wildcards, no traversal. A caller therefore claims exactly one
	// module prefix (e.g. "documents"), never a path or a foreign nested route.
	if !validCatalogIdentity(payload.Prefix) {
		httpError(w, http.StatusBadRequest, "invalid prefix")
		return true
	}

	// Guardrail 1 (binding) — the registration token authorizes exactly one
	// prefix. Reject a caller trying to register any other prefix, even with an
	// otherwise-valid token: this is the per-module binding the shared secret
	// lacked. (Both sides are already validated identity segments, so the prefix
	// is not secret and a plain compare is fine.)
	if claims.Prefix != payload.Prefix {
		httpError(w, http.StatusForbidden, "prefix not authorized for this identity")
		return true
	}

	// Guardrail 3 — catalog-protected. The generated + explicit catalog owns its
	// own /v1/<prefix> surface. Registering a colliding prefix is rejected rather
	// than stored-but-unreachable (the catalog always wins in ServeHTTP), so the
	// failure is loud instead of a silently dead route.
	if _, reserved := g.matcher.ReservedV1Prefixes()[payload.Prefix]; reserved {
		httpError(w, http.StatusConflict, "prefix reserved by catalog")
		return true
	}

	// Guardrail 4 — upstream constrained. Must parse as an http(s) URL with a
	// host, and that host must be composition-local (mesh). An external URL or a
	// public IP is rejected: a registered upstream receives forwarded bearers, so
	// it must never be able to point off-mesh.
	upstream, err := url.Parse(payload.Upstream)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
		httpError(w, http.StatusBadRequest, "invalid upstream")
		return true
	}
	if isDisallowedModuleUpstreamHost(upstream.Hostname()) {
		httpError(w, http.StatusBadRequest, "forbidden upstream host")
		return true
	}

	// Guardrail 2 (part) — first-claim-wins. A prefix already registered to a
	// different upstream cannot be taken over by a later caller sharing the
	// token; re-registering the same upstream (restart) is idempotent.
	normalized := &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}
	if _, ok := g.modules.claim(payload.Prefix, normalized); !ok {
		httpError(w, http.StatusConflict, "prefix already registered")
		return true
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
	return true
}

// handleFederatedModule proxies a /v1/<module>/* request to a runtime-registered
// module upstream. It returns true when it has answered the request. It returns
// false — leaving the caller to answer 404 — for any path that is not under a
// registered module prefix, so an unregistered prefix is indistinguishable from
// any other unexposed path.
//
// This runs only after the catalog matcher returned no match, which guarantees a
// generated or explicit route always wins over a registered prefix.
func (g *Gateway) handleFederatedModule(w http.ResponseWriter, r *http.Request) bool {
	prefix, ok := v1Prefix(r.URL.Path)
	if !ok {
		return false
	}
	upstream, ok := g.modules.get(prefix)
	if !ok {
		return false
	}

	// Same discipline as every protected catalog route: drop caller-supplied
	// identity, run ext_authz, require a valid credential, and subject the
	// forwarded request to the same rate-limit budget (below). A bearer-less
	// call is denied here, so federation only adds a proxy target — it never
	// widens the authenticated surface, and it does not open an unmetered one.
	stripAllIdentityHeaders(r)
	checkResp, err := g.authz.Check(r.Context(), buildCheckRequest(r))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "auth check failed")
		return true
	}
	if denied := checkResp.GetDeniedResponse(); denied != nil {
		code := int(denied.GetStatus().GetCode())
		if code == 0 {
			code = http.StatusForbidden
		}
		httpError(w, code, denied.GetBody())
		return true
	}
	if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
		httpError(w, http.StatusForbidden, "forbidden")
		return true
	}
	injectHeaders(r, checkResp.GetOkResponse().GetHeaders())

	// Forward the full /v1/<module>/... path unchanged: the module owns and
	// serves its own /v1/<module> surface. The caller's bearer is preserved so
	// the module can call back through the gateway on the user's behalf, exactly
	// as solution passthrough does. Service is a "module:" pseudo-name so
	// isAccountsRoute stays false and no gateway/public-origin credential is
	// stamped for a federated upstream.
	//
	// Route through rateLimitThenProxy, not proxyTo directly: a federated data
	// endpoint must consume the same per-org/per-IP budget as an equivalent
	// catalog route (e.g. /v1/users), otherwise /v1/<module>/* would be an
	// unmetered proxy an authenticated caller could flood past the org budget.
	entry := &RouteEntry{Service: moduleServicePrefix + prefix, Protected: true}
	g.rateLimitThenProxy(w, r, upstream, entry)
	return true
}

// moduleServicePrefix marks the RouteEntry.Service of a federated module route.
const moduleServicePrefix = "module:"

// isFederatedModuleRoute reports whether entry describes a runtime-registered
// module upstream (as opposed to a static catalog or solution route). Only these
// get the resolve-time-validating transport in proxyTo.
func isFederatedModuleRoute(entry *RouteEntry) bool {
	return entry != nil && strings.HasPrefix(entry.Service, moduleServicePrefix)
}

// meshHostSuffixes are the DNS suffixes that denote a composition-local
// (cluster/mesh) upstream. A hostname ending in one of these is treated as
// mesh-internal and allowed.
//
// ".localhost" is deliberately NOT here: a bare "localhost" is already allowed
// by the single-label rule below, and Go's resolver does not RFC-6761
// special-case "*.localhost" to loopback — it does a real DNS lookup. Listing
// the suffix would admit multi-label names like "evil.localhost" that resolve
// wherever their DNS points, for no legitimate mesh use.
var meshHostSuffixes = []string{
	".local", ".internal", ".svc", ".cluster.local",
}

// isDisallowedModuleUpstreamHost reports whether a module upstream host must be
// rejected. It is STRICTER than the solution guard (isForbiddenUpstreamHost):
// beyond the SSRF sinks that guard blocks, a module upstream must be
// composition-local, so a public IP or an external dotted FQDN is also rejected.
// Allowed: loopback, RFC1918/ULA private IPs, bare single-label service names
// (including "localhost"), and cluster-suffixed names.
func isDisallowedModuleUpstreamHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	// Credential-theft SSRF sinks (unspecified/link-local/metadata) and empty.
	if isForbiddenUpstreamHost(h) {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		// Loopback and private-range addresses are mesh-local; any other
		// (globally routable) IP is not composition-local and is rejected.
		if ip.IsLoopback() || ip.IsPrivate() {
			return false
		}
		return true
	}
	// A bare single-label name (no dot) is a mesh/service short name — allow it
	// (this also covers "localhost").
	if !strings.Contains(h, ".") {
		return false
	}
	// A dotted name is allowed only when it carries a known cluster suffix; a
	// public FQDN like api.example.com is rejected.
	for _, suffix := range meshHostSuffixes {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	return true
}

// --- Per-module cryptographic identity ---

// moduleRegistrationHeader carries the signed per-module registration token.
const moduleRegistrationHeader = "X-Codefly-Module-Registration"

// moduleRegistrationAudience scopes a registration token to this single purpose.
// Access tokens carry aud "saas-starter"; registration tokens carry this. Even
// though both may be minted under the same Ed25519 key, the audience check makes
// the two non-interchangeable — a stolen access token cannot register a prefix,
// and a registration token cannot authenticate a user.
const moduleRegistrationAudience = "module-registration"

// moduleRegistrationClaims is the signed assertion a module presents to register
// its prefix. `sub` is the module identity; Prefix is the single catalog-identity
// segment that identity is authorized to claim. The token binds the two
// cryptographically, so a module holding a token for "documents" cannot register
// "billing".
type moduleRegistrationClaims struct {
	jwt.RegisteredClaims
	Prefix string `json:"prefix"`
}

// verifyModuleRegistration parses and validates a module registration token with
// the same alg-locked Ed25519 discipline as an access token — same published key
// set selected by the token's kid, plus issuer, expiry, and this
// module-registration audience. It returns the verified claims. It fails closed:
// a nil ext_authz check, an unreachable or unrecognised key, an empty/bad token, a wrong
// or absent audience, or an expired token all yield ok=false.
func (s *ExtAuthz) verifyModuleRegistration(ctx context.Context, tokenString string) (*moduleRegistrationClaims, bool) {
	if s == nil || s.keys == nil || tokenString == "" {
		return nil, false
	}
	claims := &moduleRegistrationClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(moduleRegistrationAudience),
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
	return claims, true
}

// --- Registration-credential exchange ---

// moduleRegistrationTokenPath serves the exchange a module runs immediately
// before /modules/_register: it presents the registration secret its
// composition gave it and receives the signed, prefix-bound token that endpoint
// requires.
const moduleRegistrationTokenPath = "/modules/_registration-token"

// moduleSecretHeader carries the module's own registration secret. The gateway
// consumes it here and forwards it only on the internal leg, to accounts, which
// holds the digest to compare it against.
const moduleSecretHeader = "X-Codefly-Module-Secret"

// mintModuleRegistrationMethod is accounts' credential-exchange RPC. It is
// EXPOSURE_INTERNAL, so the generated mesh policy admits this gateway's service
// account to exactly this path and denies every other principal; calling it by
// full method name over the internal listener keeps that guarantee, which a
// plain HTTP route on accounts would not have (the policy allowlist is built
// from internal proto methods).
const mintModuleRegistrationMethod = "/saas.accounts.v1.ModuleCapabilitiesService/MintModuleRegistration"

// moduleRegistrationExchangeTimeout bounds the internal leg. A module blocks on
// this during startup, so a stalled accounts must surface as a failed
// registration rather than a hung boot.
const moduleRegistrationExchangeTimeout = 10 * time.Second

// handleModuleRegistrationToken serves POST /modules/_registration-token. It
// returns true when it has handled the request.
//
// The gateway brokers rather than mints: it holds only the public half of the
// signing key, and a composed module cannot reach accounts' internal listener
// itself — the mesh policy admits only this gateway's service account. So this
// handler authenticates the perimeter and forwards; accounts decides whether the
// caller owns the prefix, and this handler never learns which prefixes exist.
func (g *Gateway) handleModuleRegistrationToken(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != moduleRegistrationTokenPath {
		return false
	}
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}

	// Perimeter check. The module secret alone identifies the module, but this
	// listener has no rate limit on unrouted paths, so the cluster-internal token
	// is required too: guessing a module secret then costs an attacker the shared
	// credential first, rather than being free from anywhere that can reach the
	// gateway.
	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	secret := r.Header.Get(moduleSecretHeader)
	if secret == "" {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	var payload struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	if !validCatalogIdentity(payload.Prefix) {
		httpError(w, http.StatusBadRequest, "invalid prefix")
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), moduleRegistrationExchangeTimeout)
	defer cancel()
	issued, err := g.authz.mintModuleRegistration(ctx, payload.Prefix, secret)
	if err != nil {
		// accounts answers unknown-prefix and wrong-secret identically, so
		// relaying its refusal reveals nothing about what a composition declared.
		if status.Code(err) == codes.PermissionDenied {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return true
		}
		httpError(w, http.StatusBadGateway, "registration token unavailable")
		return true
	}

	body, err := json.Marshal(map[string]string{
		"token":     issued.GetToken(),
		"expiresAt": issued.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, "registration token unavailable")
		return true
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

// mintModuleRegistration runs the internal leg over the existing accounts
// connection, presenting the gateway's own cluster-internal credential. There is
// no vendored client stub for ModuleCapabilitiesService, so the method is
// invoked by name against generated message types shared with accounts.
func (s *ExtAuthz) mintModuleRegistration(
	ctx context.Context, prefix, secret string,
) (*accountsv1.ModuleMintRegistrationResponse, error) {
	if s.backendConn == nil {
		return nil, fmt.Errorf("accounts connection not configured")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	response := &accountsv1.ModuleMintRegistrationResponse{}
	request := &accountsv1.ModuleMintRegistrationRequest{Prefix: prefix, Secret: secret}
	if err := s.backendConn.Invoke(ctx, mintModuleRegistrationMethod, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// --- Resolve-time SSRF / DNS-rebinding defense for module upstreams ---

// moduleResolver is the DNS surface the module dial guard needs. *net.Resolver
// satisfies it; tests inject a stub to exercise rebinding deterministically.
type moduleResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// newModuleUpstreamTransport returns a reverse-proxy transport that re-validates
// a module upstream's RESOLVED address at dial time. A registrant names only a
// mesh host string, checked at register time (isDisallowedModuleUpstreamHost);
// but a name it controls can resolve off-mesh at proxy time (DNS rebinding). This
// transport resolves the host, rejects the dial unless every resolved address is
// mesh-local, then connects to a validated IP directly — never re-resolving — so
// the address dialed is exactly the one just checked.
func newModuleUpstreamTransport(resolver moduleResolver) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	// The resolve-and-pin DialContext below is the ONLY sanctioned path to a
	// module upstream. The cloned DefaultTransport inherits Proxy:
	// ProxyFromEnvironment, which would defeat that: with HTTP(S)_PROXY set the
	// transport dials the PROXY, so DialContext would validate the proxy's
	// address instead of the upstream's, and in-mesh module traffic would be
	// tunnelled through an arbitrary egress host — the exact off-mesh reach this
	// guard exists to prevent. A federated module upstream is always mesh-local
	// and must never be proxied, so disable proxying and keep the validated
	// direct dial authoritative.
	base.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if err := validateResolvedModuleAddrs(addrs); err != nil {
			return nil, err
		}
		// Pin the connection to an address we just validated: dial the resolved
		// IPs directly rather than re-resolving the hostname, so a DNS answer that
		// changes between the check and the connect cannot slip an off-mesh
		// address past the guard.
		var lastErr error
		for _, a := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return base
}

// validateResolvedModuleAddrs fails closed unless every resolved address is
// mesh-local. An empty result, or any single off-mesh address in the set,
// rejects the whole dial: a rebinding answer that mixes a benign and a forbidden
// IP must not earn a retry to the forbidden one.
func validateResolvedModuleAddrs(addrs []net.IPAddr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("module upstream did not resolve to any address")
	}
	for _, a := range addrs {
		if !isAllowedResolvedModuleIP(a.IP) {
			return fmt.Errorf("forbidden module upstream address: %s", a.IP)
		}
	}
	return nil
}

// isAllowedResolvedModuleIP is the resolve-time counterpart to the register-time
// host-string guard (isDisallowedModuleUpstreamHost): it re-checks the ACTUAL
// address a module hostname resolved to. Only loopback and private (RFC1918 /
// ULA) ranges are composition-local; the unspecified, link-local (covering the
// 169.254.169.254 cloud-metadata IP), and every globally routable address are
// rejected.
func isAllowedResolvedModuleIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
