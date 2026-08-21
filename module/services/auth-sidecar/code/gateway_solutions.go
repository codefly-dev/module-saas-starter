package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
)

// solutionRegistry holds runtime-registered solution upstreams.
//
// A "solution" is an independently deployed module the saas has no build-time
// knowledge of. Solutions self-register their upstream on startup; the gateway
// then proxies `/solutions/{id}/{path}` to them. This is a deliberate, contained
// relaxation of the otherwise static-catalog-only rule (every request must match
// an explicit catalog entry): solution routes are ALWAYS auth-required, the same
// ext_authz Check runs, and identity headers are stripped and re-stamped exactly
// as for catalog routes. The gateway performs authentication and identity
// projection; the solution's own downstream calls (e.g. accounts QueryAuditLog,
// which still enforces audit:read) remain the authorization authority.
//
// The store is process-local, exactly like the frontend's solution registry.
// For a single dev/runtime instance that is sufficient; with more than one
// sidecar replica a registration lands on one replica only, so proxy requests
// load-balanced to the others 502 until the solution re-registers there. A
// shared store (Postgres/redis), coordinated with the frontend registry, is the
// multi-replica fix and is tracked as the same follow-up.
type solutionRegistry struct {
	mu        sync.RWMutex
	upstreams map[string]*url.URL
}

func newSolutionRegistry() *solutionRegistry {
	return &solutionRegistry{upstreams: make(map[string]*url.URL)}
}

func (s *solutionRegistry) set(id string, upstream *url.URL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreams[id] = upstream
}

func (s *solutionRegistry) get(id string) (*url.URL, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	upstream, ok := s.upstreams[id]
	return upstream, ok
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
	entry := &RouteEntry{Service: "solution:" + id, UpstreamPath: "/" + path, Protected: true}
	g.proxyTo(w, r, upstream, entry)
	return true
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
