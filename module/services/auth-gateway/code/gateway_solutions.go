package main

import (
	"encoding/json"
	"net"
	"net/http"
	stdpath "path"
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
)

// Solution routing reads the durable declared registry. Every authenticated
// request uses the ordinary identity pipeline and per-viewer admission.

const solutionPrefix = "/solutions/"

const (
	solutionServicePrefix = "solution:"
	// solutionRegistrySegment serves the internal read-only snapshot.
	solutionRegistrySegment = "_registry"
)

// solutionRouteAudience is the audience a capability presented to a solution route
// must name: the host's `solution:<binding-id>` vocabulary entry for the binding
// that route resolved to.
//
// The BINDING, not the alias. An alias is deliberately reusable, so an audience
// keyed on it would let a replacement binding under the same route be addressed as
// its predecessor — the same reuse hole the artifact-approval authority and the
// boundary derivation both had to close.
//
// An empty binding id yields an empty expectation, which means "do not compare".
// That case is unreachable from here: a record with no declaration is refused
// before this, and a routable resolution always carries a binding. It is written
// this way rather than defended with a panic because the surrounding refusals are
// the real guarantee, and a nil-safe accessor beside them is not a second one.
func solutionRouteAudience(bindingID string) string {
	if bindingID == "" {
		return ""
	}
	return solutionAudiencePrefix + bindingID
}

// solutionAudiencePrefix mirrors business.SolutionAudiencePrefix in accounts. The
// gateway cannot import it — separate Go modules — so the two are pinned to each
// other by a test rather than left to agree by eye.
const solutionAudiencePrefix = "solution:"

// solutionIDPattern mirrors the identity rule the registry validates: one
// lowercase segment usable as a path element and a routing key.
var solutionIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$`)

// handleSolutionRequest serves the `/solutions/*` surface. It returns true when
// it has handled the request (the caller must then return). Any other path is
// left to the static route matcher.
func (g *Gateway) handleSolutionRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, solutionPrefix) {
		return false
	}
	rest := strings.TrimPrefix(r.URL.Path, solutionPrefix)

	switch rest {
	case solutionRegistrySegment:
		g.handleSolutionRegistrySnapshot(w, r)
		return true
	}

	// The per-viewer control segments dispatch from their own file, so that what
	// remains BELOW in this one is the proxy decision alone — registered, active,
	// authenticated, routed — with no reference to any tenant's installations. That
	// separation is load-bearing: route and page exposure stays deployment-wide
	// while the projections do not, and module/tools' boundary test reads this file
	// to hold the first half of that (SOLUTION_REGISTRATION.md §4).
	if g.handleSolutionViewerSegment(w, r, rest) {
		return true
	}

	id, path, _ := strings.Cut(rest, "/")
	if !solutionIDPattern.MatchString(id) {
		httpError(w, http.StatusNotFound, "solution not specified")
		return true
	}
	// ONE resolution, carried from here to the forward. Admission is decided on
	// routing.TargetID and traffic goes to routing.Upstream, and both are read
	// from the same record in the same lookup — see solutionRouting. A second
	// read further down this handler is what let a replacement binding be
	// admitted on its own installation while the bearer went to its
	// predecessor's address.
	routing, resolution := g.solutions.resolveRouting(r.Context(), id)
	switch resolution {
	case solutionUnregistered:
		httpError(w, http.StatusBadGateway, "solution not registered")
		return true
	case solutionNotActive:
		// Registered but not serving: a half is absent, the
		// halves disagree on a contract version, or delivery withdrew it. This is
		// deliberately not the "not registered" answer — the distinction is what
		// tells an operator whether to look for a missing deployment or a
		// misbehaving one.
		httpError(w, http.StatusServiceUnavailable, "solution registration not active")
		return true
	case solutionWrongKind:
		// The alias is declared, and not as a SOLUTION — a module, or a record
		// whose kind this gateway cannot read. Same answer and same code as a
		// record nothing declared, for the same reason: from this surface it is
		// not a solution this host declares, and no later read changes that. A
		// module is reached at /v1/<alias>/*, which is where its own refusal is
		// worded.
		httpError(w, http.StatusForbidden, "solution is not declared on this host")
		return true
	case solutionRegistryUnavailable:
		// No snapshot has ever loaded, so this replica cannot tell an
		// unregistered solution from a registered one. Fail closed and say so.
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
		return true
	}

	// THE ROUTE IS RESOLVED, SO THE AUDIENCE IS KNOWN: a capability presented to
	// this route must name THIS binding. Derived from the same carried resolution
	// that chose the upstream, so the token cannot be judged against one binding
	// while traffic goes to another's address.
	//
	// This is what makes A-for-B a 403-with-the-audience-error rather than a
	// forward: a capability minted for `solution:<other-binding>` verified cleanly
	// here before, and only the callee could notice.
	//
	// IT IS NOT AN AUTHORITY CHECK, and the per-viewer installation admission below
	// stays load-bearing. The mint holds a requested audience to the host's closed
	// vocabulary, which makes an audience PLAUSIBLE — but it ties it to the caller
	// only when that caller carries an audience ceiling, and accounts'
	// enforceActorAudience treats an empty ceiling as unrestricted while the actor
	// is nil for every owner-only mint. So a viewer can hold a capability naming a
	// binding it was never installed against, and what refuses the request is the
	// admission call, not this comparison. Deleting that admission because "the
	// audience already covers it" would remove the only check that does.
	if g.rejectInvalidWorkContext(w, r, solutionRouteAudience(routing.BindingID)) {
		return true
	}

	// A solution's static Module-Federation surface — the MF manifest, remote
	// entry, and JS chunks under /assets, plus the public /.well-known documents —
	// is fetched by the browser's module loader with no bearer, exactly as the
	// solution origin itself serves it (open, permissive CORS). Auth-gating those
	// script/manifest fetches 401s them and makes the remote impossible to load
	// same-origin through the host. Serve the public GET surface unauthenticated,
	// with caller identity still stripped; every other path (the solution's data
	// endpoints, e.g. /example) stays auth-required below. The upstream is sent
	// the same cleaned path the exemption was decided on, so the two can't diverge.
	//
	// Rate-limit budget: this public GET surface carries an IP-keyed, fail-open
	// budget, exactly like every public catalog route — all of which run through
	// rateLimitThenProxy (see the "public" case in Gateway.ServeHTTP). Because the
	// caller identity was just stripped there is no X-Org-Id, so the limiter keys
	// on the client IP; Public is not one of the classes that fail closed (only
	// the authentication/MFA budgets do — see routing_authz_artifact.go), so a
	// limiter-backend outage still lets assets load. At the production budget
	// (1000/min/key) a module loader's handful of chunk fetches is nowhere near
	// the cap, so legitimate same-origin loads are unaffected while an
	// unauthenticated flood of this proxy is still capped. Leaving it on bare
	// proxyTo would make it the one unmetered public proxy in the gateway (#513).
	if publicPath, ok := solutionPublicUpstreamPath(r.Method, path); ok {
		if routing.TargetID == "" {
			// Presence nothing declared. This fetch carries no credential, so
			// there is no VIEWER to ask the authority about — but there is still
			// an installation question with a final answer, and it needs no
			// viewer: a registration no declaration opened a target for cannot
			// have been installed by any organisation, so nobody consented to
			// this host serving its bytes. Until this check, such a record's
			// remote entry and chunks were served same-origin and executed
			// inside the host origin under its own `'self'`, while the
			// authenticated surface beside it refused the same record outright.
			//
			// A verdict (403), not an outage: the record is here, it is active,
			// and it is declared by nothing — no later read changes that.
			httpError(w, http.StatusForbidden, "solution is not declared on this host")
			return true
		}
		stripAllIdentityHeaders(r)
		entry := &RouteEntry{
			Service:        "solution:" + id,
			UpstreamPath:   publicPath,
			RateLimitClass: edgeRateLimitClassPublic,
		}
		g.rateLimitThenProxy(w, r, routing.Upstream, entry)
		return true
	}

	// Same identity discipline as every protected route: drop caller-supplied
	// identity, run ext_authz, and require a valid credential.
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
		// The discovery challenge rides on a 401 and ONLY on a 401.
		//
		// An MCP client discovers how to authenticate from the 401 itself: the
		// challenge names where the resource describes itself, that document
		// names this host as its authorization server, and the client then runs
		// the authorization-code flow. A 401 without it is a dead end.
		//
		// But `error="invalid_token"` is an assertion about the caller's
		// credential, and RFC 6750 §3 has a client read the challenge on a 403
		// as well as a 401 — so putting it on an answer that is not about the
		// credential tells a client to re-authenticate when re-authenticating
		// cannot help. On the 503 this gateway returns when a revocation store
		// is unavailable, that would mean every refused request triggers a
		// fresh discovery and authorization round against this host's own
		// endpoints, adding load while a dependency is already down.
		//
		// Stamped here rather than before the check, so there is no state to
		// undo on the paths that must not carry it: the 500 above, the 403
		// below, and every served response.
		if code == http.StatusUnauthorized {
			stampResourceChallenge(w, id, g.authz.publicBase)
		}
		httpError(w, code, denied.GetBody())
		return true
	}
	if int(checkResp.GetStatus().GetCode()) != int(codes.OK) {
		httpError(w, http.StatusForbidden, "forbidden")
		return true
	}
	injectHeaders(r, checkResp.GetOkResponse().GetHeaders())

	// AVAILABLE is not INSTALLED. Delivery (or, until the cutover, a
	// registration) makes a solution available on this deployment; an
	// organization installing it and a grant reaching the viewer are what make
	// it usable. Those are three layers and none is inferred from another, so
	// the proxy asks the authority before it forwards anything — including the
	// caller's own bearer, which is the part that matters: without this check a
	// viewer in any organization reached the data endpoints of every solution
	// the deployment runs, and the solution received a real bearer for them.
	//
	// It is deliberately AFTER ext_authz. The identity this is decided on must
	// be the verified one, and during an impersonation it must be the
	// impersonated viewer — the subject accounts authorizes against — rather
	// than the administrator acting. Both come from what the check just stamped.
	//
	// The public GET surface above is not gated PER VIEWER and cannot be: it is
	// fetched by the browser's module loader with no credential, so there is no
	// viewer to ask about. What it serves is the solution's own static
	// Module-Federation bytes, which carry no tenant data; the authenticated
	// surface below is where an organization's admission is enforced.
	//
	// It is not declaration-blind, though, and the two were conflated for as
	// long as "there is no viewer" was treated as "nothing can be checked". A
	// record nothing declared is refused above, because that answer needs no
	// viewer.
	org := r.Header.Get("X-Org-Id")
	viewer := effectiveViewer(r)
	if org == "" || viewer == "" {
		// Authenticated, but the session names no organization. There is no
		// org-scoped admission to read, so this is refused rather than routed —
		// the same answer, for the same reason, as the entitlement listing.
		w.Header().Set(solutionEntitlementRefusalHeader, refusalNoOrganization)
		httpError(w, http.StatusForbidden, "no organization in this session")
		return true
	}
	// Proxy to the solution. The caller's bearer is preserved so the solution
	// can call accounts through the gateway on the user's behalf.
	//
	// Route through rateLimitThenProxy, not proxyTo directly: an authenticated
	// solution data endpoint must consume the same per-org budget as an equivalent
	// catalog route (e.g. /v1/users), otherwise /solutions/<id>/* would be an
	// unmetered proxy an authenticated caller could flood past the org budget.
	//
	// Budget class and failure mode are chosen to match that equivalent catalog
	// data route, not left to the zero value. A solution data endpoint is the
	// StandardRead/StandardWrite tier (read vs write by method, as the catalog
	// classes its own routes), keyed on the injected X-Org-Id. It deliberately
	// fails OPEN on a limiter-backend outage: the gateway's class policy fails
	// closed only for the authentication and MFA budgets (see the class-derived
	// rateLimitBackendFailClosed in routing_authz_artifact.go), so a data route
	// that failed closed here would 503 authenticated traffic on every Redis blip
	// — stricter than any /v1/* catalog data route and inconsistent with policy.
	// Same fix as the federated-module half (#512).
	class := edgeRateLimitClassStandardRead
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		class = edgeRateLimitClassStandardWrite
	}
	entry := &RouteEntry{
		Service:        "solution:" + id,
		UpstreamPath:   "/" + path,
		Protected:      true,
		RateLimitClass: class,
	}
	// Metered FIRST, and the admission check runs inside the budget.
	//
	// The authority call is the expensive part of this path — up to one
	// scope-tree read per entitlement page, per request — and it costs the same
	// whether the request is forwarded, refused as not entitled, or abandoned
	// because accounts is down. Admitting before metering therefore left an
	// authenticated caller an unmetered way to spend that cost: ask about a
	// solution it is not entitled to, as fast as it likes, and every refusal was
	// free. The entitlement LISTING already wrapped its work this way; the proxy
	// did not.
	g.rateLimitThenServe(w, r, entry, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch g.admitViewerSolution(r.Context(), org, viewer, routing) {
		case viewerSolutionNotEntitled:
			// A verdict on this organization's admission, named so a client can
			// tell it from an ext_authz denial on the credential: one is fixed by
			// installing and granting, the other by signing in again.
			w.Header().Set(solutionEntitlementRefusalHeader, refusalNotEntitled)
			httpError(w, http.StatusForbidden, "solution is not installed for this organization")
			return
		case viewerSolutionUndecidable:
			// The authority could not be asked. 503 and nothing forwarded:
			// routing would serve a solution nobody has confirmed is installed,
			// and 403 would tell an operator the grant is missing when accounts
			// is simply down.
			httpError(w, http.StatusServiceUnavailable, "solution entitlement authority unavailable")
			return
		}
		// The destination is the one this request's single resolution named, and
		// the one whose identity the authority just admitted.
		g.proxyTo(w, r, routing.Upstream, entry)
	}))
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
// `assets/../example` from borrowing the /assets exemption to reach an
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

// solutionRegistryProjection is what the gateway publishes about the registry.
// It is a deliberate projection, not the record: the upstream URL stays inside
// this process, because the only component that routes to it is this one.
type solutionRegistryProjection struct {
	Revision  int64                            `json:"revision"`
	Solutions []solutionRegistrationProjection `json:"solutions"`
}

type solutionRegistrationProjection struct {
	ID        string `json:"id"`
	Publisher string `json:"publisher"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
	// TargetID is the immutable solution target this record is declared under —
	// the identity an installation names, and therefore the key a consumer joins
	// its entitlements against. Empty for a record nothing declared, which no
	// entitlement can ever match.
	//
	// It is published because the alternative is what was here before: the
	// consumer joined on `id`, the route alias, which a later binding may claim —
	// so a replacement solution inherited the predecessor's menu entry and its
	// team exposure. The alias stays in the projection because it is what a URL
	// is built from; it is no longer what anything is authorised by.
	TargetID     string  `json:"targetId,omitempty"`
	ServiceAlias string  `json:"serviceAlias,omitempty"`
	Manifest     *string `json:"manifest,omitempty"`
}

// handleSolutionRegistrySnapshot serves this replica's view of the registry to
// the frontend (which rebuilds its own cache from it) and to an operator. It
// answers 503 rather than an empty list when no snapshot has ever loaded, so
// "nothing is registered" is never confused with "this replica cannot see the
// registry".
func (g *Gateway) handleSolutionRegistrySnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	records, revision, loaded := g.solutions.snapshot()
	if !loaded {
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
		return
	}
	out := solutionRegistryProjection{
		Revision:  revision,
		Solutions: make([]solutionRegistrationProjection, 0, len(records)),
	}
	for _, record := range records {
		projection := solutionRegistrationProjection{
			ID:           record.GetSolutionId(),
			Publisher:    record.GetPublisher(),
			Revision:     record.GetRevision(),
			Status:       solutionRegistryStatusLabel(record),
			TargetID:     record.GetDeclared().GetTargetId(),
			ServiceAlias: record.GetBackend().GetServiceAlias(),
		}
		if manifest := record.GetFrontend().GetManifest(); manifest != "" {
			projection.Manifest = &manifest
		}
		out.Solutions = append(out.Solutions, projection)
	}
	writeSolutionJSON(w, http.StatusOK, out)
}

func writeSolutionJSON(w http.ResponseWriter, code int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
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
