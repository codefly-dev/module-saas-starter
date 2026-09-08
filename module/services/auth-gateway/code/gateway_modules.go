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
// Trust model: registration is gated on the cluster-internal token — a single
// SHARED secret (see Sidecar.acceptsInternalToken), the same credential the
// frontend and solutions present. There is no per-module cryptographic identity,
// so the gateway cannot prove a caller "owns" the prefix it registers. The
// enforceable guardrails below are therefore: authenticated (internal token),
// well-formed single-segment prefix, first-claim-wins (a prefix already held by
// a different upstream cannot be taken over), never shadowing the catalog, and a
// composition-local (mesh) upstream only. Stronger per-caller binding would
// require per-module credentials and is a deliberate follow-up, mirroring the
// solution registry's documented shared-token limitation.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/grpc/codes"
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

	// Guardrail 1 — authenticated. Registration decides where authenticated
	// module traffic (bearer + injected identity) is forwarded, so it requires
	// the cluster-internal token. acceptsInternalToken fails closed on an
	// empty/unset credential, so an unauthenticated edge caller can never point
	// a prefix at an attacker-controlled upstream and harvest forwarded bearers.
	if g.sidecar == nil || !g.sidecar.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
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
	checkResp, err := g.sidecar.Check(r.Context(), buildCheckRequest(r))
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
	entry := &RouteEntry{Service: "module:" + prefix, Protected: true}
	g.rateLimitThenProxy(w, r, upstream, entry)
	return true
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
