package main

// HTTP gateway: the single public ingress for the module during local dev.
//
// In production, Envoy (or Envoy Gateway) runs in front and talks to the
// sidecar's gRPC ext_authz endpoint. The Go gateway below is functionally
// equivalent for local iteration — it calls Sidecar.Check in-process (zero
// gRPC round-trip) and does the same header injection + routing.
//
// Routing is WHITELIST-ONLY: backend endpoints come from generated and explicit
// REST catalogs; frontend pages come from the generated Next.js page catalog,
// with finite allowances for Next.js assets and route handlers. Everything else
// returns 404.

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc/codes"
)

// Gateway is an HTTP reverse proxy that delegates auth decisions to a
// Sidecar (in-process) and forwards allowed requests to upstream services.
// Route matching is driven entirely by the static RouteMatcher — no prefix
// matching, no implicit exposure.
type Gateway struct {
	sidecar           *Sidecar
	matcher           *RouteMatcher
	upstreams         map[string]*url.URL // service name → upstream URL
	selfHandler       http.Handler        // handler for "self" routes (health checks)
	rateLimiter       *RateLimiter
	requiredUpstreams []string
}

// NewGateway constructs a gateway with explicit route matching.
// upstreams maps service names (from routes.codefly.yaml) to their URLs.
// rateLimiter may be nil to disable rate limiting.
func NewGateway(sidecar *Sidecar, matcher *RouteMatcher, upstreams map[string]*url.URL, rateLimiter *RateLimiter) *Gateway {
	g := &Gateway{
		sidecar:           sidecar,
		matcher:           matcher,
		upstreams:         upstreams,
		rateLimiter:       rateLimiter,
		requiredUpstreams: matcher.RequiredServices(),
	}
	if !containsString(g.requiredUpstreams, "frontend") {
		g.requiredUpstreams = append(g.requiredUpstreams, "frontend")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.healthHandler)
	mux.HandleFunc("/ready", g.readyHandler)
	// Backwards compat: any other self-route returns basic OK.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	g.selfHandler = mux
	return g
}

// healthHandler is process-only liveness. Dependency state belongs to /ready.
func (g *Gateway) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// readyHandler returns 200 when the sidecar is ready to serve traffic.
// Every service referenced by the exact route catalog must be configured and
// reachable; a partial deployment must not receive traffic.
func (g *Gateway) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if g.sidecar == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not ready","reason":"sidecar not initialized"}`)
		return
	}

	if len(g.requiredUpstreams) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not ready","reason":"no routed upstreams"}`)
		return
	}

	for _, name := range g.requiredUpstreams {
		u, configured := g.upstreams[name]
		if !configured {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"status":"not ready","reason":"missing upstream","service":%q}`, name))
			return
		}
		addr := u.Host
		if addr == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"status":"not ready","reason":"invalid upstream","service":%q}`, name))
			return
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			log.Printf("readiness: upstream %s (%s) unreachable: %v", name, addr, err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"status":"not ready","reason":"upstream unreachable","service":%q}`, name))
			return
		}
		conn.Close()
	}

	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// ServeHTTP runs one request through: route match → auth check → reverse proxy.
// Unlisted paths are rejected with 404. Every request must match an explicit
// entry in the route config.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry := g.matcher.Match(r.Method, r.URL.Path)
	if entry == nil {
		entry = matchFrontendGatewayRoute(r.Method, r.URL.Path)
	}
	if entry == nil {
		log.Printf("WARN: blocked request: method=%s path=%s reason=no_matching_route", r.Method, r.URL.Path)
		httpError(w, http.StatusNotFound, "endpoint not exposed")
		return
	}

	// Self-service routes (health checks) — no auth, no proxy.
	if entry.Service == "self" {
		g.selfHandler.ServeHTTP(w, r)
		return
	}

	// Identity and trust credentials are never accepted from the public side
	// of the gateway. Sidecar.Check only sees the caller's real credential
	// (Authorization); successful checks re-stamp canonical headers below.
	stripAllIdentityHeaders(r)

	// Resolve upstream.
	upstream, ok := g.upstreams[entry.Service]
	if !ok {
		log.Printf("WARN: blocked request: method=%s path=%s reason=no_upstream_for_service_%s", r.Method, r.URL.Path, entry.Service)
		httpError(w, http.StatusBadGateway, "upstream not configured for service")
		return
	}

	// Auth check based on the route's auth requirement.
	switch authMode(entry) {
	case "public":
		// Public route: call Check to get identity headers if a token is present,
		// but don't reject if no token.
		checkResp, err := g.sidecar.Check(r.Context(), buildCheckRequest(r))
		if err != nil {
			httpError(w, http.StatusInternalServerError, "auth check failed")
			return
		}

		hasAuth := r.Header.Get("authorization") != ""

		if denied := checkResp.GetDeniedResponse(); denied != nil {
			if hasAuth {
				// Caller presented a bad token — reject even on public routes.
				code := int(denied.GetStatus().GetCode())
				if code == 0 {
					code = http.StatusForbidden
				}
				httpError(w, code, denied.GetBody())
				return
			}
			// No token on public route — forward without identity.
			stripAllIdentityHeaders(r)
			g.rateLimitThenProxy(w, r, upstream, entry)
			return
		}

		if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
			if hasAuth {
				httpError(w, http.StatusForbidden, "forbidden")
				return
			}
			stripAllIdentityHeaders(r)
			g.rateLimitThenProxy(w, r, upstream, entry)
			return
		}

		// Token present and valid — inject identity headers.
		injectHeaders(r, checkResp.GetOkResponse().GetHeaders())
		g.rateLimitThenProxy(w, r, upstream, entry)

	case "required":
		// Protected route: must have valid auth.
		checkResp, err := g.sidecar.Check(r.Context(), buildCheckRequest(r))
		if err != nil {
			httpError(w, http.StatusInternalServerError, "auth check failed")
			return
		}

		if denied := checkResp.GetDeniedResponse(); denied != nil {
			code := int(denied.GetStatus().GetCode())
			if code == 0 {
				code = http.StatusForbidden
			}
			httpError(w, code, denied.GetBody())
			return
		}

		if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
			httpError(w, http.StatusForbidden, "forbidden")
			return
		}

		injectHeaders(r, checkResp.GetOkResponse().GetHeaders())
		g.rateLimitThenProxy(w, r, upstream, entry)

	case "mfa_pending":
		// MFA pending route: sidecar handles mfa_token validation.
		// For now, delegate to the same Check flow — the sidecar accepts
		// mfa_token on these paths.
		checkResp, err := g.sidecar.Check(r.Context(), buildCheckRequest(r))
		if err != nil {
			httpError(w, http.StatusInternalServerError, "auth check failed")
			return
		}

		if denied := checkResp.GetDeniedResponse(); denied != nil {
			code := int(denied.GetStatus().GetCode())
			if code == 0 {
				code = http.StatusForbidden
			}
			httpError(w, code, denied.GetBody())
			return
		}

		if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
			httpError(w, http.StatusForbidden, "forbidden")
			return
		}

		injectHeaders(r, checkResp.GetOkResponse().GetHeaders())
		g.rateLimitThenProxy(w, r, upstream, entry)

	default:
		log.Printf("WARN: blocked request: method=%s path=%s reason=unknown_auth_type_%s", r.Method, r.URL.Path, authMode(entry))
		httpError(w, http.StatusInternalServerError, "unknown auth type")
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// rateLimitThenProxy applies rate limiting (if configured) then proxies.
func (g *Gateway) rateLimitThenProxy(w http.ResponseWriter, r *http.Request, upstream *url.URL, entry *RouteEntry) {
	if g.rateLimiter != nil {
		g.rateLimiter.Middleware(limiterFailureModeFor(entry), entry.AuthenticationFactorAttempt, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.proxyTo(w, r, upstream, entry)
		})).ServeHTTP(w, r)
		return
	}
	g.proxyTo(w, r, upstream, entry)
}

func limiterFailureModeFor(entry *RouteEntry) limiterFailureMode {
	if entry == nil {
		return limiterFailClosed
	}
	if entry.RateLimitBackendFailClosed {
		return limiterFailClosed
	}
	return limiterFailOpen
}

func (g *Gateway) proxyTo(w http.ResponseWriter, r *http.Request, upstream *url.URL, entry *RouteEntry) {
	if entry != nil && entry.UpstreamPath != "" {
		r.URL.Path = entry.UpstreamPath
		r.URL.RawPath = ""
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		httpError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}
	proxy.ServeHTTP(w, r)
}

// buildCheckRequest translates an incoming *http.Request into an Envoy
// ext_authz CheckRequest. Headers are lowercased and joined, same as
// Envoy would present them to the ext_authz server.
func buildCheckRequest(r *http.Request) *authv3.CheckRequest {
	headers := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		if len(v) == 0 {
			continue
		}
		headers[strings.ToLower(k)] = strings.Join(v, ",")
	}
	return &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Method:  r.Method,
					Path:    r.URL.Path,
					Host:    r.Host,
					Headers: headers,
				},
			},
		},
	}
}

// injectHeaders writes the ext_authz OkResponse headers onto the incoming
// request before forwarding to the upstream. Existing header values are
// replaced — the sidecar is authoritative for identity.
func injectHeaders(r *http.Request, headers []*corev3.HeaderValueOption) {
	stripAllIdentityHeaders(r)
	for _, h := range headers {
		key := h.GetHeader().GetKey()
		val := h.GetHeader().GetValue()
		if key == "" {
			continue
		}
		r.Header.Set(key, val)
	}
}

// stripAllIdentityHeaders removes every identity header from the request.
// Used on public routes when no auth was presented — prevents a caller
// from spoofing identity by setting headers directly.
func stripAllIdentityHeaders(r *http.Request) {
	for _, k := range untrustedAuthHeaders {
		r.Header.Del(k)
	}
}

var untrustedAuthHeaders = []string{
	"x-user-id", "x-org-id", "x-org-role", "x-platform-role", "x-roles",
	"x-auth-id", "x-user-email", "x-user-name", "x-session-id",
	"x-acting-as-user-id", "x-scopes", "x-mfa-satisfied",
	"x-authentication-methods", "x-auth-time", "x-assurance-level", "x-mfa-verified-at",
	"x-codefly-gateway-token", "x-codefly-internal-token",
}

// httpError writes a plain-text error response. Bodies are short,
// machine-readable errors — no leaking of implementation details.
func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg)
}

// MustURL parses a url.URL or panics; used in wire-up only.
func MustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("gateway: invalid upstream %q: %v", s, err))
	}
	return u
}
