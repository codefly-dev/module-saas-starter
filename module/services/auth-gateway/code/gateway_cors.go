package main

// Cross-origin access for registered clients (handbook track 0016).
//
// The host's own frontend reaches the gateway through a server-side proxy, so
// it never needed cross-origin access and the gateway never answered any: no
// OPTIONS, no access-control-allow-origin. That made the proxy the only door,
// and a second client could only get in by holding the same shared secret —
// which is the arrangement the boundary rules forbid.
//
// What opens the door is not a relaxation but an identity. A registered client
// is named by the `azp` of the bearer it presents, and the registry says which
// origins that client speaks from, so the gateway can grant exactly those and
// refuse the rest. Requests whose token names no client — the host's own web
// sessions, API keys, service traffic — are untouched by everything here: they
// take the same path, with the same headers, as before the registry existed.
// That matters because the frontend's proxy copies the browser's Origin header
// through verbatim, so a host page's own same-origin POST arrives here bearing
// an Origin the registry has never heard of.
//
// Credentials are deliberately not allowed. A registered client authenticates
// with the bearer it was issued; echoing access-control-allow-credentials would
// additionally let a cross-origin page ride the host's session cookie, which is
// a door nobody asked for.

import (
	"net/http"
	"strings"
)

const (
	corsAllowMethods = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS"
	// The request headers a client needs to authenticate, describe a body,
	// resume a stream, make a write idempotent, and carry trace context.
	// Anything else is refused at preflight rather than mirrored back, so the
	// granted surface stays the one the host designed.
	//
	// idempotency-key is not optional: several procedures declare
	// IDEMPOTENCY_REQUIREMENT_REQUIRED, and a browser omits any header the
	// preflight did not allow — so leaving it out does not make those calls
	// awkward, it makes them impossible, and no change on the client side can
	// recover them.
	corsAllowHeaders = "authorization, content-type, accept, last-event-id, " +
		"idempotency-key, traceparent, tracestate"
	// x-request-id is what correlates a client-side failure with the gateway's
	// own log line; without exposing it the client cannot read it.
	corsExposeHeaders = "x-request-id"
	corsMaxAge        = "600"
)

// isCORSPreflight reports whether this is the browser's permission question
// rather than the request itself. Access-Control-Request-Method is what
// distinguishes the two: a bare OPTIONS is an ordinary request.
func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != ""
}

// handleCORSPreflight answers a registered origin's preflight and reports
// whether it did. It runs before routing, because the path a preflight names
// has not been authorized yet and the answer does not depend on it.
//
// An origin the registry does not know is left alone entirely: the request
// falls through to the router, which has no OPTIONS route and refuses it, which
// is what the gateway did before any client was registered.
func (g *Gateway) handleCORSPreflight(w http.ResponseWriter, r *http.Request) bool {
	if !isCORSPreflight(r) {
		return false
	}
	origin := r.Header.Get("Origin")
	if !g.clients.originRegistered(r.Context(), origin) {
		return false
	}
	if !grantableCrossOrigin(
		r.Header.Get("Access-Control-Request-Method"),
		r.URL.Path,
		requestsAuthorizationHeader(r.Header.Get("Access-Control-Request-Headers")),
	) {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", corsAllowMethods)
	h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
	h.Set("Access-Control-Max-Age", corsMaxAge)
	addVary(h, "Origin")
	addVary(h, "Access-Control-Request-Headers")
	w.WriteHeader(http.StatusNoContent)
	return true
}

// grantableCrossOrigin reports whether the request a preflight describes could
// actually be granted, so the answer never promises access the request path
// then withholds.
//
// One surface cannot keep the promise: a solution's public sub-paths are served
// with identity stripped and with no ext_authz check at all, so the gateway
// never learns which client a bearer there belongs to and has nothing to bind.
// A caller that intends to send one is told no now, rather than having its
// request served and its response discarded by the browser. Without a bearer
// that same surface is granted normally — it is unauthenticated by design, and
// the origin is the whole of what either half judges it on.
func grantableCrossOrigin(method, path string, wantsAuthorization bool) bool {
	if !wantsAuthorization {
		return true
	}
	rest, isSolution := strings.CutPrefix(path, solutionPrefix)
	if !isSolution {
		return true
	}
	_, subPath, _ := strings.Cut(rest, "/")
	_, public := solutionPublicUpstreamPath(method, subPath)
	return !public
}

// requestsAuthorizationHeader reports whether a preflight's
// Access-Control-Request-Headers list names Authorization. The list is
// comma-separated and lowercased by the browser, but it is caller-supplied, so
// it is parsed rather than trusted to be either.
func requestsAuthorizationHeader(requested string) bool {
	for _, name := range strings.Split(requested, ",") {
		if strings.EqualFold(strings.TrimSpace(name), "authorization") {
			return true
		}
	}
	return false
}

// authorizeCrossOrigin binds a request that ext_authz resolved to a registered
// client against the origin it was made from, and grants that origin the CORS
// headers. It returns the ResponseWriter to serve the rest of the request with,
// and false when it has already refused.
//
// It runs at the point every forwarded request passes through, whatever route
// matched it, so a client cannot reach the solution passthrough, a federated
// module prefix, or a catalog route from an origin it did not register.
func (g *Gateway) authorizeCrossOrigin(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return w, true
	}

	if clientID := r.Header.Get(clientIDHeader); clientID != "" {
		if !g.clients.registers(r.Context(), clientID, origin) {
			// The token names a client, and the client does not speak from
			// here — a token issued to one client replayed from another's
			// page, or from somewhere the registry has never heard of. Refuse
			// before the request reaches an upstream, so it has no effect
			// rather than merely an unreadable response. No access-control
			// headers: this origin was granted nothing.
			httpError(w, http.StatusForbidden, "origin not registered for this client")
			return nil, false
		}
		return &corsResponseWriter{ResponseWriter: w, origin: origin}, true
	}

	// No client was resolved, which is two different situations.
	//
	// A request carrying a credential that names no client is the host's own
	// web session or an API key. It is granted nothing, exactly as before the
	// registry existed — load-bearing, because the frontend's proxy copies the
	// browser's Origin header through verbatim, so the host's own same-site
	// writes arrive here carrying an origin the registry has never heard of.
	//
	// A request carrying no credential at all is a client that does not have
	// one yet: obtaining the first token is itself a cross-origin call, made
	// before there is any `azp` to bind to. Requiring a bearer to be granted
	// the response would leave a registered client able to sign in and unable
	// to ever read its token. The origin is the only thing such a request can
	// be judged on, and it is the same thing the preflight was judged on.
	if credentialPresented(r) {
		return w, true
	}
	if !g.clients.originRegistered(r.Context(), origin) {
		return w, true
	}
	return &corsResponseWriter{ResponseWriter: w, origin: origin}, true
}

// credentialPresented reports whether the caller supplied anything that could
// identify it. Cookie counts: a cross-origin page cannot attach one (the
// gateway never allows credentials), so a request bearing one reached here
// through the frontend's server-side proxy and belongs to the host's own
// session, not to a client bootstrapping.
func credentialPresented(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != ""
}

// corsResponseWriter stamps the grant on the way out. It has to run at
// WriteHeader rather than up front because the reverse proxy copies the
// upstream's own response headers into this map first, and an upstream that
// answers CORS itself — a solution serving its origin permissively — would
// otherwise leave two access-control-allow-origin values, which every browser
// rejects. The gateway is the authority for what it granted, so it replaces
// whatever the upstream said.
type corsResponseWriter struct {
	http.ResponseWriter
	origin  string
	stamped bool
}

func (w *corsResponseWriter) WriteHeader(status int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(status)
}

func (w *corsResponseWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

func (w *corsResponseWriter) stamp() {
	if w.stamped {
		return
	}
	w.stamped = true
	h := w.Header()
	for key := range h {
		if strings.HasPrefix(strings.ToLower(key), "access-control-") {
			h.Del(key)
		}
	}
	h.Set("Access-Control-Allow-Origin", w.origin)
	h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
	addVary(h, "Origin")
}

// addVary appends a field name to Vary unless it is already listed. Adding it
// blindly duplicates whatever an upstream already said it varies on, and the
// gateway proxies upstreams that answer CORS themselves.
func addVary(h http.Header, field string) {
	for _, existing := range h.Values("Vary") {
		for _, name := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(name), field) {
				return
			}
		}
	}
	h.Add("Vary", field)
}

// Flush keeps an authenticated event stream incremental, and stamps first so a
// streamed response carries the grant with its first byte. It is declared even
// though Unwrap would reach the real writer, because an http.ResponseController
// finds a Flusher on the outermost writer before it unwraps — which is the only
// way the stamp gets ahead of the stream.
func (w *corsResponseWriter) Flush() {
	w.stamp()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap is how an http.ResponseController reaches the capabilities this
// wrapper does not implement itself — hijacking for a protocol upgrade, and the
// write deadlines the reverse proxy sets while streaming. Without it a
// registered client's cross-origin request could not be upgraded at all, since
// the proxy refuses to switch protocols on a writer it cannot hijack.
func (w *corsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
