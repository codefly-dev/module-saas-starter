package main

// HTTP gateway: the single public ingress for the module during local dev.
//
// In production, Envoy (or Envoy Gateway) runs in front and talks to the
// sidecar's gRPC ext_authz endpoint. The Go gateway below is functionally
// equivalent for local iteration — it calls Sidecar.Check in-process (zero
// gRPC round-trip) and does the same header injection + routing.
//
// Routing is WHITELIST-ONLY: every endpoint must be explicitly listed in
// routes.codefly.yaml. Unlisted paths return 404. Zero prefix matching.

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
	sidecar     *Sidecar
	matcher     *RouteMatcher
	upstreams   map[string]*url.URL // service name → upstream URL
	selfHandler http.Handler        // handler for "self" routes (health checks)
	rateLimiter *RateLimiter
}

// NewGateway constructs a gateway with explicit route matching.
// upstreams maps service names (from routes.codefly.yaml) to their URLs.
// rateLimiter may be nil to disable rate limiting.
func NewGateway(sidecar *Sidecar, matcher *RouteMatcher, upstreams map[string]*url.URL, rateLimiter *RateLimiter) *Gateway {
	g := &Gateway{
		sidecar:     sidecar,
		matcher:     matcher,
		upstreams:   upstreams,
		rateLimiter: rateLimiter,
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

// healthHandler returns 200 with {"status":"ok"} when the sidecar is
// running, or 503 if the gRPC auth server is not wired.
func (g *Gateway) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if g.sidecar == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"unhealthy","reason":"sidecar not initialized"}`)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// readyHandler returns 200 when the sidecar is ready to serve traffic.
// In addition to the basic health check it verifies that at least one
// upstream is configured (so we can actually proxy requests).
func (g *Gateway) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if g.sidecar == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not ready","reason":"sidecar not initialized"}`)
		return
	}

	if len(g.upstreams) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not ready","reason":"no upstreams configured"}`)
		return
	}

	// Verify at least one upstream is reachable (non-blocking TCP dial).
	anyReachable := false
	for name, u := range g.upstreams {
		addr := u.Host
		if addr == "" {
			continue
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			log.Printf("readiness: upstream %s (%s) unreachable: %v", name, addr, err)
			continue
		}
		conn.Close()
		anyReachable = true
		break
	}
	if !anyReachable {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not ready","reason":"no upstream reachable"}`)
		return
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
		log.Printf("WARN: blocked request: method=%s path=%s reason=no_matching_route", r.Method, r.URL.Path)
		httpError(w, http.StatusNotFound, "endpoint not exposed")
		return
	}

	// Self-service routes (health checks) — no auth, no proxy.
	if entry.Service == "self" {
		g.selfHandler.ServeHTTP(w, r)
		return
	}

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
			g.rateLimitThenProxy(w, r, upstream)
			return
		}

		if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
			if hasAuth {
				httpError(w, http.StatusForbidden, "forbidden")
				return
			}
			stripAllIdentityHeaders(r)
			g.rateLimitThenProxy(w, r, upstream)
			return
		}

		// Token present and valid — inject identity headers.
		injectHeaders(r, checkResp.GetOkResponse().GetHeaders())
		g.rateLimitThenProxy(w, r, upstream)

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
		g.rateLimitThenProxy(w, r, upstream)

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
		g.rateLimitThenProxy(w, r, upstream)

	default:
		log.Printf("WARN: blocked request: method=%s path=%s reason=unknown_auth_type_%s", r.Method, r.URL.Path, authMode(entry))
		httpError(w, http.StatusInternalServerError, "unknown auth type")
	}
}

// rateLimitThenProxy applies rate limiting (if configured) then proxies.
func (g *Gateway) rateLimitThenProxy(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
	if g.rateLimiter != nil {
		g.rateLimiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.proxyTo(w, r, upstream)
		})).ServeHTTP(w, r)
		return
	}
	g.proxyTo(w, r, upstream)
}

func (g *Gateway) proxyTo(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
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
	for _, h := range headers {
		key := h.GetHeader().GetKey()
		val := h.GetHeader().GetValue()
		if key == "" {
			continue
		}
		r.Header.Set(key, val)
	}
	// Strip any incoming identity headers that the caller might have
	// set maliciously — the sidecar is the only legitimate source.
	// If the sidecar didn't set one of these, we must not forward it.
	stripIfAbsent(r, headers, "x-user-id")
	stripIfAbsent(r, headers, "x-org-id")
	stripIfAbsent(r, headers, "x-org-role")
	stripIfAbsent(r, headers, "x-platform-role")
	stripIfAbsent(r, headers, "x-session-id")
	stripIfAbsent(r, headers, "x-acting-as-user-id")
}

func stripIfAbsent(r *http.Request, headers []*corev3.HeaderValueOption, key string) {
	for _, h := range headers {
		if strings.EqualFold(h.GetHeader().GetKey(), key) {
			return // sidecar set it; keep
		}
	}
	r.Header.Del(key)
}

// stripAllIdentityHeaders removes every identity header from the request.
// Used on public routes when no auth was presented — prevents a caller
// from spoofing identity by setting headers directly.
func stripAllIdentityHeaders(r *http.Request) {
	for _, k := range []string{
		"x-user-id", "x-org-id", "x-org-role", "x-platform-role",
		"x-session-id", "x-acting-as-user-id", "x-scopes",
	} {
		r.Header.Del(k)
	}
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
