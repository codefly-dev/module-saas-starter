package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
)

// upstreamRegistry is a process-local map of a routing key → upstream URL,
// populated at runtime by self-registration rather than at build time.
//
// It backs two distinct self-registration surfaces that share the same
// discipline (see gateway_solutions.go for solutions and gateway_modules.go for
// composed-module REST federation): an independently deployed component the saas
// has no build-time knowledge of POSTs its upstream on startup, and the gateway
// then proxies matching requests to it. This is a deliberate, contained
// relaxation of the otherwise static-catalog-only rule (every request must match
// an explicit catalog entry): proxied data endpoints stay auth-required, the
// same ext_authz Check runs, and identity headers are stripped and re-stamped
// exactly as for catalog routes. The gateway performs authentication and
// identity projection; the registered upstream's own downstream calls remain the
// authorization authority.
//
// The store is process-local, exactly like the frontend's solution registry.
// For a single dev/runtime instance that is sufficient; with more than one
// sidecar replica a registration lands on one replica only, so proxy requests
// load-balanced to the others 502 until the component re-registers there. A
// shared store (Postgres/redis), coordinated with the frontend registry, is the
// multi-replica fix and is tracked as the same follow-up.
type upstreamRegistry struct {
	mu        sync.RWMutex
	upstreams map[string]*url.URL
}

func newUpstreamRegistry() *upstreamRegistry {
	return &upstreamRegistry{upstreams: make(map[string]*url.URL)}
}

// set unconditionally writes key→upstream, LAST-write-wins: a re-registration
// with a different upstream overwrites the previous one. This is the OPPOSITE
// of claim (below), which is first-claim-wins and refuses a takeover. Use set
// only where a later registration is meant to supersede an earlier one (solution
// re-registration); use claim wherever a key must not be silently stealable by a
// later caller sharing the cluster-internal token (module federation).
func (s *upstreamRegistry) set(id string, upstream *url.URL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreams[id] = upstream
}

func (s *upstreamRegistry) get(id string) (*url.URL, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	upstream, ok := s.upstreams[id]
	return upstream, ok
}

// claim registers key→upstream when the key is free, or when it already points
// at the same upstream (idempotent re-registration on restart). It returns
// (existing, false) without mutating when the key is already held by a DIFFERENT
// upstream: first-claim-wins, so a later caller sharing the cluster-internal
// token cannot silently take over a key another component already registered.
func (s *upstreamRegistry) claim(id string, upstream *url.URL) (*url.URL, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.upstreams[id]; ok {
		if existing.String() != upstream.String() {
			return existing, false
		}
		return existing, true
	}
	s.upstreams[id] = upstream
	return upstream, true
}

const solutionPrefix = "/solutions/"

// handleSolutionRequest serves the `/solutions/*` surface. It returns true when
// it has handled the request (the caller must then return). Any other path is
// left to the static route matcher.
func (g *Gateway) handleSolutionRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, solutionPrefix) {
		return false
	}
	rest := strings.TrimPrefix(r.URL.Path, solutionPrefix)

	// Internal upstream registration. This mutates the proxy's upstream table,
	// so it is authenticated with the cluster-internal token (see
	// handleSolutionRegister) rather than trusting network placement alone.
	if rest == "_register" {
		g.handleSolutionRegister(w, r)
		return true
	}

	id, path, _ := strings.Cut(rest, "/")
	if id == "" {
		httpError(w, http.StatusNotFound, "solution not specified")
		return true
	}
	upstream, ok := g.solutions.get(id)
	if !ok {
		httpError(w, http.StatusBadGateway, "solution not registered")
		return true
	}

	// A solution's static Module-Federation surface — the MF manifest, remote
	// entry, and JS chunks under /assets, plus the public /.well-known documents —
	// is fetched by the browser's module loader with no bearer, exactly as the
	// solution origin itself serves it (open, permissive CORS). Auth-gating those
	// script/manifest fetches 401s them and makes the remote impossible to load
	// same-origin through the host. Serve the public GET surface unauthenticated,
	// with caller identity still stripped; every other path (the solution's data
	// endpoints, e.g. /lastlogin) stays auth-required below. The upstream is sent
	// the same cleaned path the exemption was decided on, so the two can't diverge.
	//
	// Rate-limit budget: this public GET surface is deliberately left exempt (it
	// proxies via proxyTo, not rateLimitThenProxy). It is identity-stripped and
	// carries no user data, so there is no per-org budget to attach it to, and
	// the org/IP key rateLimitThenProxy would use is meaningless here. Metering it
	// on an IP key risks blocking legitimate same-origin asset/manifest loads for
	// the browser module loader — a fail-open static surface should stay loadable.
	// The authenticated data path below is where the budget is enforced (#513),
	// mirroring the federated-module fix (#512), which metered only its data route.
	if publicPath, ok := solutionPublicUpstreamPath(r.Method, path); ok {
		stripAllIdentityHeaders(r)
		entry := &RouteEntry{Service: "solution:" + id, UpstreamPath: publicPath}
		g.proxyTo(w, r, upstream, entry)
		return true
	}

	// Same identity discipline as every protected route: drop caller-supplied
	// identity, run ext_authz, and require a valid credential.
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

	// Proxy to the solution. The caller's bearer is preserved so the solution
	// can call accounts through the gateway on the user's behalf.
	//
	// Route through rateLimitThenProxy, not proxyTo directly: an authenticated
	// solution data endpoint must consume the same per-org/per-IP budget as an
	// equivalent catalog route (e.g. /v1/users), otherwise /solutions/<id>/* would
	// be an unmetered proxy an authenticated caller could flood past the org
	// budget. Same fix as the federated-module half (#512).
	entry := &RouteEntry{Service: "solution:" + id, UpstreamPath: "/" + path, Protected: true}
	g.rateLimitThenProxy(w, r, upstream, entry)
	return true
}

// solutionPublicPrefixes are the path segments a solution serves openly: its
// static Module-Federation assets (/assets/*) and public capability/discovery
// documents (/.well-known/*). These carry no user data, so the gateway exempts
// unauthenticated reads of them — matching the solution origin, which already
// serves them without a bearer.
var solutionPublicPrefixes = []string{"assets", ".well-known"}

// solutionPublicUpstreamPath reports whether a solution sub-path (the suffix
// after /solutions/{id}/, with no leading slash) is part of the solution's
// public, unauthenticated read surface, and returns the canonical upstream path
// to forward. Both the decision and the returned path are derived from the
// cleaned path: deciding on the clean prevents a traversal suffix like
// `assets/../lastlogin` from borrowing the /assets exemption to reach an
// authenticated endpoint, and forwarding that same clean prevents an upstream
// that resolves the raw path differently from being handed a path the gateway
// never classified as public.
func solutionPublicUpstreamPath(method, subPath string) (string, bool) {
	if method != http.MethodGet && method != http.MethodHead {
		return "", false
	}
	clean := stdpath.Clean("/" + subPath)
	for _, prefix := range solutionPublicPrefixes {
		if clean == "/"+prefix || strings.HasPrefix(clean, "/"+prefix+"/") {
			return clean, true
		}
	}
	return "", false
}

func (g *Gateway) handleSolutionRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Registration is a privileged mutation, not a public endpoint: it decides
	// where authenticated solution traffic (bearer + injected identity) gets
	// forwarded. Require the cluster-internal token — the same credential the
	// frontend presents to establish a trusted origin — so an unauthenticated
	// edge caller cannot register an attacker-controlled upstream and harvest
	// forwarded bearers. acceptsInternalToken fails closed on an empty/unset
	// credential.
	if g.sidecar == nil || !g.sidecar.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var payload struct {
		ID       string `json:"id"`
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if payload.ID == "" {
		httpError(w, http.StatusBadRequest, "missing id")
		return
	}
	upstream, err := url.Parse(payload.Upstream)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
		httpError(w, http.StatusBadRequest, "invalid upstream")
		return
	}
	// Defence in depth against a confused or compromised internal caller: never
	// let a solution upstream point at the cloud metadata endpoint or another
	// link-local/unspecified address (the credential-theft SSRF sinks reachable
	// from inside the mesh). Loopback is deliberately allowed — local
	// `codefly run` solutions self-register loopback upstreams, and a deployed
	// upstream is a cluster DNS name, never link-local.
	if isForbiddenUpstreamHost(upstream.Hostname()) {
		httpError(w, http.StatusBadRequest, "forbidden upstream host")
		return
	}
	g.solutions.set(payload.ID, &url.URL{Scheme: upstream.Scheme, Host: upstream.Host})
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// isForbiddenUpstreamHost blocks upstream hosts that are credential-theft SSRF
// sinks: the unspecified address, link-local addresses (which cover the
// 169.254.169.254 cloud metadata IP), and the well-known metadata hostname.
// Loopback is intentionally NOT blocked (see caller).
func isForbiddenUpstreamHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == "metadata.google.internal" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsUnspecified() ||
			ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast()
	}
	return false
}
