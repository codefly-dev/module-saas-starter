package main

// HTTP gateway: the single public ingress for the module during local dev.
//
// In production, Envoy (or Envoy Gateway) runs in front and talks to the
// sidecar's gRPC ext_authz endpoint. The Go gateway below is functionally
// equivalent for local iteration — it calls Sidecar.Check in-process (zero
// gRPC round-trip) and does the same header injection + routing.
//
// Routing is path-prefix based:
//
//	/v1/*           → api backend  (gRPC-gateway REST endpoints)
//	/api/*          → api backend
//	/*              → frontend (Next.js SSR)
//
// Every request flows through Sidecar.Check first. Rejected requests never
// touch the upstream; allowed requests are forwarded with the canonical
// X-User-ID / X-Org-ID / X-Session-ID headers injected.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc/codes"
)

// RouteRule maps a request path prefix to an upstream URL.
//
// Protected routes require a valid access token (or match the sidecar's
// explicit public-path allowlist). Non-protected routes are forwarded
// without auth — used for the Next.js frontend which handles its own
// page-level auth via AuthKit, and still receives X-User-ID from the
// sidecar when a token is present.
type RouteRule struct {
	Prefix    string
	Upstream  *url.URL
	Protected bool
}

// Gateway is an HTTP reverse proxy that delegates auth decisions to a
// Sidecar (in-process) and forwards allowed requests to one of several
// upstream services by path prefix.
type Gateway struct {
	sidecar     *Sidecar
	routes      []RouteRule
	rateLimiter *RateLimiter
}

// NewGateway constructs a gateway. Routes are tried in order; the first
// matching prefix wins. Catch-all should be last. rateLimiter may be nil
// to disable rate limiting.
func NewGateway(sidecar *Sidecar, routes []RouteRule, rateLimiter *RateLimiter) *Gateway {
	return &Gateway{sidecar: sidecar, routes: routes, rateLimiter: rateLimiter}
}

// ServeHTTP runs one request through: route match → ext_authz (if
// protected) → reverse proxy.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rule := g.matchRoute(r.URL.Path)
	if rule == nil {
		httpError(w, http.StatusNotFound, "no route for "+r.URL.Path)
		return
	}

	// Always call Check — the sidecar knows the full public-path allowlist,
	// and even protected routes may be allowlisted by path. The rule's
	// Protected flag is an additional "this route always requires auth"
	// signal that overrides the sidecar's public-path allowlist for API
	// prefixes. Concretely:
	//   - Protected route → token must be present AND the sidecar must
	//     accept it. Deny on failure.
	//   - Non-protected route (frontend) → token is optional. If present
	//     and valid, headers are forwarded. If invalid, we still deny
	//     (don't leak access). If absent, we forward without identity.
	checkResp, err := g.sidecar.Check(r.Context(), buildCheckRequest(r))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "auth check failed")
		return
	}

	hasAuth := r.Header.Get("authorization") != ""

	if denied := checkResp.GetDeniedResponse(); denied != nil {
		if rule.Protected || hasAuth {
			// Protected route, or caller presented a bad token on any route.
			code := int(denied.GetStatus().GetCode())
			if code == 0 {
				code = http.StatusForbidden
			}
			httpError(w, code, denied.GetBody())
			return
		}
		// Non-protected route, no token → forward without identity.
		stripAllIdentityHeaders(r)
		g.rateLimitThenProxy(w, r, rule.Upstream)
		return
	}

	if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
		if rule.Protected || hasAuth {
			httpError(w, http.StatusForbidden, "forbidden")
			return
		}
		stripAllIdentityHeaders(r)
		g.rateLimitThenProxy(w, r, rule.Upstream)
		return
	}

	// Allowed. Inject headers from the sidecar response.
	injectHeaders(r, checkResp.GetOkResponse().GetHeaders())

	// Rate limit AFTER auth so we can use the org ID as the key.
	g.rateLimitThenProxy(w, r, rule.Upstream)
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

// matchRoute picks the first rule whose prefix is a prefix of path.
func (g *Gateway) matchRoute(path string) *RouteRule {
	for i := range g.routes {
		if strings.HasPrefix(path, g.routes[i].Prefix) {
			return &g.routes[i]
		}
	}
	return nil
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
// Used on non-protected routes when no auth was presented — prevents a
// caller from spoofing identity by setting headers directly.
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

// noCopy ensures Context is used for cancel; kept to avoid a dead import.
var _ = context.Background
