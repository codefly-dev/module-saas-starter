package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"regexp"
	"strings"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// The runtime solution surface: /solutions/* proxying, plus the internal
// registration endpoints both halves of a solution register through.
//
// An independently deployed component the saas has no build-time knowledge of
// POSTs its upstream on startup and the gateway then proxies matching requests
// to it. This is a deliberate, contained relaxation of the otherwise
// static-catalog-only rule (every request must match an explicit catalog
// entry): proxied data endpoints stay auth-required, the same ext_authz Check
// runs, and identity headers are stripped and re-stamped exactly as for catalog
// routes. The gateway performs authentication and identity projection; the
// registered upstream's own downstream calls remain the authorization
// authority.
//
// Registrations are durable and shared (see gateway_solution_registry.go): they
// live in accounts, not in this process, so they survive a restart and every
// replica converges on the same revision. What this file owns is the perimeter
// — who may register, what an upstream may point at — and the proxy itself.

const solutionPrefix = "/solutions/"

const (
	// solutionRegisterPath registers (POST) or deregisters (DELETE) a
	// solution's backend half — the upstream this gateway proxies to.
	solutionRegisterSegment = "_register"
	// solutionFrontendSegment registers the frontend half. The frontend has no
	// route to the accounts internal listener, so it hands its validated
	// manifest to the gateway, which is already the broker for that listener.
	solutionFrontendSegment = "_frontend"
	// solutionRegistrySegment reads this replica's snapshot: the frontend
	// rebuilds its own cache from it, and an operator reads it to tell an
	// unregistered solution from a pending, expired, or removed one.
	solutionRegistrySegment = "_registry"
)

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

	// Internal registration and registry reads. These mutate or expose the
	// proxy's routing state, so they are authenticated with the
	// cluster-internal token rather than trusting network placement alone.
	switch rest {
	case solutionRegisterSegment:
		g.handleSolutionRegister(w, r)
		return true
	case solutionFrontendSegment:
		g.handleSolutionFrontendRegister(w, r)
		return true
	case solutionRegistrySegment:
		g.handleSolutionRegistrySnapshot(w, r)
		return true
	}

	id, path, _ := strings.Cut(rest, "/")
	if id == "" {
		httpError(w, http.StatusNotFound, "solution not specified")
		return true
	}
	upstream, resolution := g.solutions.resolve(r.Context(), id)
	switch resolution {
	case solutionUnregistered:
		httpError(w, http.StatusBadGateway, "solution not registered")
		return true
	case solutionNotActive:
		// Registered but not serving: a half never arrived, a lease lapsed, the
		// halves disagree on a contract version, or it was deregistered. This is
		// deliberately not the "not registered" answer — the distinction is what
		// tells an operator whether to look for a missing deployment or a
		// misbehaving one.
		httpError(w, http.StatusServiceUnavailable, "solution registration not active")
		return true
	case solutionRegistryUnavailable:
		// No snapshot has ever loaded, so this replica cannot tell an
		// unregistered solution from a registered one. Fail closed and say so.
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
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
		stripAllIdentityHeaders(r)
		entry := &RouteEntry{
			Service:        "solution:" + id,
			UpstreamPath:   publicPath,
			RateLimitClass: edgeRateLimitClassPublic,
		}
		g.rateLimitThenProxy(w, r, upstream, entry)
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

// solutionRegistrationBody is the shared shape of both registration endpoints.
// Only one of the halves' fields is read by each handler; sharing the envelope
// keeps identity, ownership, and the compare-and-swap controls identical on
// both, which is the point of having one record behind them.
type solutionRegistrationBody struct {
	ID        string `json:"id"`
	Publisher string `json:"publisher"`
	// ExpectedRevision lets a registrant that tracks its own revision drive the
	// compare-and-swap itself. Left unset, the gateway supplies the revision it
	// last saw for the record, which is what makes the pre-existing
	// `{id, upstream}` registration call keep working unchanged.
	ExpectedRevision *int64 `json:"expectedRevision"`
	// Reactivate is the explicit re-registration of a deregistered solution. A
	// plain retry from a retiring deployment does not set it, so it cannot
	// resurrect what an operator removed.
	Reactivate      bool   `json:"reactivate"`
	ContractVersion string `json:"contractVersion"`

	// Backend half.
	Upstream     string `json:"upstream"`
	ServiceAlias string `json:"serviceAlias"`

	// Frontend half. The manifest is carried as the exact JSON text the
	// frontend validated, not as a nested object: the gateway stores it
	// verbatim and never reinterprets it, and byte stability is what lets the
	// registry recognise a re-registration as a lease renewal rather than a
	// change.
	Manifest string `json:"manifest"`
}

// decodeSolutionRegistration reads and validates the parts of a registration
// body common to both halves. It writes the error response itself and returns
// ok=false when the caller must stop.
func (g *Gateway) decodeSolutionRegistration(w http.ResponseWriter, r *http.Request) (*solutionRegistrationBody, bool) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return nil, false
	}
	// Registration is a privileged mutation, not a public endpoint: it decides
	// where authenticated solution traffic (bearer + injected identity) gets
	// forwarded and what the host renders as an in-origin remote. Require the
	// cluster-internal token — the same credential the frontend presents to
	// establish a trusted origin — so an unauthenticated edge caller cannot
	// register an attacker-controlled upstream and harvest forwarded bearers.
	// acceptsInternalToken fails closed on an empty/unset credential.
	if !g.acceptsSolutionRegistrationCredential(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	body := &solutionRegistrationBody{}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSolutionRegistrationBytes)).Decode(body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return nil, false
	}
	if !solutionIDPattern.MatchString(body.ID) {
		httpError(w, http.StatusBadRequest, "missing id")
		return nil, false
	}
	// Publisher defaults to the solution id. Registration is authenticated by
	// the shared cluster-internal token, which attests to no particular
	// publisher, so this binding is first-claim-wins on the id rather than a
	// verified identity; A13 (#540) replaces the default with one.
	if body.Publisher == "" {
		body.Publisher = body.ID
	}
	return body, true
}

// maxSolutionRegistrationBytes bounds a registration body. A frontend manifest
// carries a nav entry, a remote descriptor, and an optional dashboard graph;
// 256 KiB is far above any of those and matches what the registry accepts.
const maxSolutionRegistrationBytes = 256 << 10

func (g *Gateway) acceptsSolutionRegistrationCredential(r *http.Request) bool {
	return g.authz != nil && g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token"))
}

// handleSolutionRegister serves the backend half: POST registers or renews the
// upstream, DELETE deregisters the whole solution.
func (g *Gateway) handleSolutionRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		g.handleSolutionDeregister(w, r)
		return
	}
	body, ok := g.decodeSolutionRegistration(w, r)
	if !ok {
		return
	}
	upstream, err := url.Parse(body.Upstream)
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
	alias := body.ServiceAlias
	if alias == "" {
		alias = body.ID
	}
	req := g.newSolutionPut(body)
	req.Half = &accountsv1.PutSolutionRegistrationRequest_Backend{
		Backend: &accountsv1.SolutionBackendRegistration{
			// Normalized to scheme+host: the registry stores the routing target,
			// not whatever path or query the registrant happened to send.
			Upstream:        (&url.URL{Scheme: upstream.Scheme, Host: upstream.Host}).String(),
			ServiceAlias:    alias,
			ContractVersion: body.ContractVersion,
		},
	}
	g.submitSolutionPut(w, r, req, body.Reactivate)
}

// handleSolutionFrontendRegister serves the frontend half. The frontend cannot
// reach the accounts internal listener itself — the mesh policy admits only
// this gateway's service account — so it registers through here, exactly as a
// composed module obtains its registration credential through here.
func (g *Gateway) handleSolutionFrontendRegister(w http.ResponseWriter, r *http.Request) {
	body, ok := g.decodeSolutionRegistration(w, r)
	if !ok {
		return
	}
	// The manifest's contents are the frontend's business; the gateway checks
	// only that it was given a document, so a truncated or empty body fails
	// here rather than being stored as a valid-looking half.
	if !json.Valid([]byte(body.Manifest)) {
		httpError(w, http.StatusBadRequest, "invalid manifest")
		return
	}
	req := g.newSolutionPut(body)
	req.Half = &accountsv1.PutSolutionRegistrationRequest_Frontend{
		Frontend: &accountsv1.SolutionFrontendRegistration{
			Manifest:        body.Manifest,
			ContractVersion: body.ContractVersion,
		},
	}
	g.submitSolutionPut(w, r, req, body.Reactivate)
}

// newSolutionPut builds the part of a registry write both halves share,
// including the compare-and-swap token when the caller did not supply one. The
// caller fills in Half.
func (g *Gateway) newSolutionPut(body *solutionRegistrationBody) *accountsv1.PutSolutionRegistrationRequest {
	return &accountsv1.PutSolutionRegistrationRequest{
		SolutionId:       body.ID,
		Publisher:        body.Publisher,
		LeaseSeconds:     uint32(solutionLease.Seconds()),
		ExpectedRevision: g.solutionExpectedRevision(body),
	}
}

// submitSolutionPut sends the write and renders its outcome.
func (g *Gateway) submitSolutionPut(
	w http.ResponseWriter, r *http.Request, req *accountsv1.PutSolutionRegistrationRequest, reactivate bool,
) {
	record, err := g.solutions.write(r.Context(), req, reactivate)
	if err != nil {
		writeSolutionRegistryError(w, err)
		return
	}
	g.writeSolutionRegistrationResult(w, record)
}

// solutionExpectedRevision picks the compare-and-swap token for a write.
//
// An explicit token from the registrant always wins. Otherwise the gateway
// supplies the revision it last saw, which is what turns the unchanged
// `{id, upstream}` call into a correct read-modify-write. The one case it
// deliberately withholds a token is a tombstoned record without `reactivate`:
// the registry then refuses the write, which is exactly how a delayed retry
// from a retired deployment is stopped from recreating a removed registration.
func (g *Gateway) solutionExpectedRevision(body *solutionRegistrationBody) *int64 {
	if body.ExpectedRevision != nil {
		return body.ExpectedRevision
	}
	return g.solutions.expectedRevisionFor(body.ID, body.Reactivate)
}

// handleSolutionDeregister tombstones a registration. Both halves go away
// together: a solution is removed as a unit, so there is no window where the
// page survives its backend or the other way round.
func (g *Gateway) handleSolutionDeregister(w http.ResponseWriter, r *http.Request) {
	if !g.acceptsSolutionRegistrationCredential(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.URL.Query().Get("id")
	if !solutionIDPattern.MatchString(id) {
		httpError(w, http.StatusBadRequest, "missing id")
		return
	}
	record, err := g.solutions.remove(r.Context(), id)
	if err != nil {
		writeSolutionRegistryError(w, err)
		return
	}
	g.writeSolutionRegistrationResult(w, record)
}

// solutionRegistryProjection is what the gateway publishes about the registry.
// It is a deliberate projection, not the record: the upstream URL stays inside
// this process, because the only component that routes to it is this one.
type solutionRegistryProjection struct {
	Revision  int64                            `json:"revision"`
	Solutions []solutionRegistrationProjection `json:"solutions"`
}

type solutionRegistrationProjection struct {
	ID           string  `json:"id"`
	Publisher    string  `json:"publisher"`
	Revision     int64   `json:"revision"`
	Status       string  `json:"status"`
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
	if !g.acceptsSolutionRegistrationCredential(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	records, revision, loaded := g.solutions.snapshot()
	if !loaded {
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
		return
	}
	now := g.solutions.now()
	out := solutionRegistryProjection{
		Revision:  revision,
		Solutions: make([]solutionRegistrationProjection, 0, len(records)),
	}
	for _, record := range records {
		projection := solutionRegistrationProjection{
			ID:           record.GetSolutionId(),
			Publisher:    record.GetPublisher(),
			Revision:     record.GetRevision(),
			Status:       solutionRegistryStatusLabel(record, now),
			ServiceAlias: record.GetBackend().GetServiceAlias(),
		}
		if manifest := record.GetFrontend().GetManifest(); manifest != "" {
			projection.Manifest = &manifest
		}
		out.Solutions = append(out.Solutions, projection)
	}
	writeSolutionJSON(w, http.StatusOK, out)
}

// writeSolutionRegistrationResult echoes the revision the write landed at, so
// a registrant can hold it and drive its own compare-and-swap next time, and
// the status, so it learns immediately that its half alone is not yet serving.
func (g *Gateway) writeSolutionRegistrationResult(w http.ResponseWriter, record *accountsv1.SolutionRegistration) {
	writeSolutionJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       record.GetSolutionId(),
		"revision": record.GetRevision(),
		"status":   solutionRegistryStatusLabel(record, g.solutions.now()),
	})
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

// writeSolutionRegistryError maps the registry's refusals onto HTTP. The
// conflict cases are distinguished from an outage so a registrant can tell "you
// are out of date, re-read and retry" from "the registry is down, back off".
func writeSolutionRegistryError(w http.ResponseWriter, err error) {
	if errors.Is(err, errSolutionRegistryUnconfigured) {
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
		return
	}
	switch grpcstatus.Code(err) {
	case codes.Aborted:
		httpError(w, http.StatusConflict, "registration revision conflict")
	case codes.FailedPrecondition:
		httpError(w, http.StatusConflict, "registration conflicts with current state")
	case codes.PermissionDenied:
		httpError(w, http.StatusForbidden, "solution is registered to another publisher")
	case codes.NotFound:
		httpError(w, http.StatusNotFound, "solution not registered")
	case codes.InvalidArgument:
		httpError(w, http.StatusBadRequest, "invalid registration")
	default:
		httpError(w, http.StatusBadGateway, "solution registry unavailable")
	}
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
