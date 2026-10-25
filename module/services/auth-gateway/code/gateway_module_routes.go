package main

// Routing a DECLARED module (issue #952).
//
// A composed module is reached at /v1/<alias>/*, and the alias comes from the
// same place a solution's does: a presence document delivery handed this host,
// reconciled into the declared registry. Nothing self-registers. main's
// federation was the opposite — a module POSTed its own upstream to
// /modules/_register and the gateway believed it — which this branch deletes, so
// at the head before this file a composed module was not reachable over REST at
// all.
//
// Declaration supplies the destination; accounts supplies organization consent
// and viewer exposure on every request. Neither fact substitutes for the other.

import (
	"net/http"
	"strings"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
)

// moduleCapabilitiesAudience mirrors business.ModuleCapabilitiesAudience in
// accounts — the audience a capability addressed to the module capability surface
// carries. The gateway cannot import it (separate Go modules), so the two are
// pinned to each other by a test rather than left to agree by eye.
const moduleCapabilitiesAudience = "module-capabilities"

// moduleServicePrefix marks the RouteEntry.Service of a declared module route.
// It is a pseudo-service name, so isAccountsRoute stays false and no
// gateway/public-origin credential is stamped for a module upstream.
const moduleServicePrefix = "module:"

// v1Prefix extracts the `<prefix>` from a `/v1/<prefix>/...` (or `/v1/<prefix>`)
// path. It reports false for any path not under /v1/ or with an empty prefix.
//
// A custom-verb route ends its first segment with `:<verb>`
// (`/v1/permissions:check`, `/v1/work-contexts:renew`), and the verb is not part
// of the prefix. Splitting only on `/` made `permissions:check` the "prefix" of
// that route, which left the real prefix `permissions` absent from
// ReservedV1Prefixes — and made the lookup below compare a verb-suffixed segment
// against declared aliases, so `/v1/<module>:<verb>` could never reach a module
// that owns the prefix.
func v1Prefix(path string) (string, bool) {
	const root = "/v1/"
	if !strings.HasPrefix(path, root) {
		return "", false
	}
	rest := path[len(root):]
	prefix := rest
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		prefix = rest[:idx]
	}
	if idx := strings.IndexByte(prefix, ':'); idx >= 0 {
		prefix = prefix[:idx]
	}
	if prefix == "" {
		return "", false
	}
	return prefix, true
}

// handleDeclaredModule proxies a /v1/<alias>/* request to the upstream a declared
// module binding named. It returns true when it has answered.
//
// It runs ONLY after the catalog matcher found no route, so a generated or
// explicit route always wins over a declared alias. A path under no /v1/ prefix,
// and a prefix no record holds, return false and fall through to the caller's
// 404: an alias nothing declared stays indistinguishable from any other unexposed
// path, which is what keeps this from being a probe for which modules a
// deployment runs.
func (g *Gateway) handleDeclaredModule(w http.ResponseWriter, r *http.Request) bool {
	alias, ok := v1Prefix(r.URL.Path)
	if !ok {
		return false
	}
	// A gateway wired with no registry client serves no declared modules AT ALL,
	// which is not the same fact as a registry that is down: there is no authority
	// here that could have been asked. Fall through to the caller's 404, so a
	// deployment without the declared surface answers an unexposed path exactly as
	// it did before this surface existed.
	if g.solutions == nil || g.solutions.client == nil {
		return false
	}
	routing, resolution := g.solutions.resolveModuleRouting(r.Context(), alias)
	switch resolution {
	case solutionUnregistered:
		// Nothing declares this alias, or a tombstone withdrew it. Fall through
		// to the 404 the caller already answers for an unexposed path.
		return false
	case solutionWrongKind:
		// The alias IS declared, and as something this surface does not serve: a
		// solution, or a record whose kind this gateway cannot read. A verdict
		// (403), not an outage and not a 404 — the record is here and it is not a
		// module, and no later read changes that.
		//
		// Deliberately not a fall-through to 404: a solution reached here would
		// otherwise look unexposed while being served, with its per-viewer
		// admission, two paths away.
		httpError(w, http.StatusForbidden, "module is not declared on this host")
		return true
	case solutionNotActive:
		// Declared but not serving: a half is absent or the halves disagree on a
		// contract version. Distinct from "not declared" because it tells an
		// operator to look for a deployment rather than for a delivery.
		httpError(w, http.StatusServiceUnavailable, "module registration not active")
		return true
	case solutionRegistryUnavailable:
		// No snapshot has ever loaded, so this replica cannot tell a declared
		// module from an unexposed path. Fail closed and say so, rather than
		// answering 404 — which would read as "this host does not serve that".
		httpError(w, http.StatusServiceUnavailable, "solution registry unavailable")
		return true
	case solutionRegistryStale:
		// A snapshot loaded, and the registry has not confirmed it within the
		// revocation lease: a withdrawal applied since would be invisible here,
		// so nothing is routed on it.
		httpError(w, http.StatusServiceUnavailable, "solution registry snapshot is stale")
		return true
	}

	// An alias the CATALOG owns is refused rather than served in part. The catalog
	// matcher ran first and took every path it matches, so a declared module
	// sharing one of its prefixes would serve only the paths the catalog happens
	// not to match — a surface split between two authorities that nobody
	// declared. main refused such a claim when a module registered itself; with
	// declaration there is no registration to refuse here, so the refusal that
	// belongs upstream (in what a delivery may declare) is at least not silently
	// a half-served prefix.
	if _, reserved := g.matcher.ReservedV1Prefixes()[alias]; reserved {
		httpError(w, http.StatusForbidden, "module alias is owned by this host's own catalog")
		return true
	}

	// THE ROUTE IS RESOLVED, SO THE AUDIENCE IS KNOWN — and this route serves TWO
	// kinds of consumer, so two audiences are permitted and no more.
	//
	//   - moduleCapabilitiesAudience: a declared module's OWN identity, minted by
	//     accounts' StartModuleTask from the module's service credential.
	//   - the alias: a VIEWER's delegated capability for this module. Accounts
	//     refuses to mint the capability audience from any caller-supplied mint
	//     (requireMintableAudience — a context addressed to it is read as a module
	//     identity), so the audience a person's capability for this module can
	//     carry is the module's own name, which is the name this route was
	//     resolved on and the one a consuming runtime declares and asks for.
	//
	// Expecting ONLY the capability audience here was therefore an expectation no
	// viewer capability could satisfy: with the edge verifier wired, every
	// delegated capability presented at a declared module route was refused.
	// Listing both is not a widening — a solution's audience is still refused
	// here, which is the A-for-B rule this derivation exists for.
	capability, rejected := g.verifiedWorkContext(w, r, moduleCapabilitiesAudience, alias)
	if rejected {
		return true
	}

	// TWO ARMS from here. A request with no bearer and no cookie, on the
	// workload endpoint, presenting a capability for THIS alias, is a composed
	// workload's headless operation context: there is no person, so the person
	// path below cannot admit it, and gateway_workload_operation.go decides it
	// from the capability's signed tenant and seal. Everything else takes the
	// person path, unchanged.
	if selectsWorkloadOperation(r, capability, alias) {
		g.serveWorkloadOperation(w, r, alias, routing, capability)
		return true
	}

	// The person path. Same identity discipline as every protected route: drop
	// caller-supplied identity, run ext_authz, require a valid credential. A
	// bearer-less call that the arm above did not select is denied here, so on
	// this path a declared module adds a proxy target and never widens the
	// authenticated surface.
	//
	// The verified capability is carried INTO the check, because it is what lets a
	// resource-confined credential be admitted here at all: ext_authz pairs the
	// two and refuses the credential when no capability of this person's accompanies
	// it (gateway_module_capability.go carries the whole rule). A deployment with
	// no edge verifier records nothing, so such a credential stays refused — the
	// same direction as every other asymmetry between this path and Envoy's.
	stripAllIdentityHeaders(r)
	ctx := withPresentedModuleCapability(r.Context(), capability, alias)
	checkResp, err := g.authz.Check(ctx, buildCheckRequest(r))
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

	// Forward the full /v1/<alias>/... path unchanged: the module owns and serves
	// its own /v1/<alias> surface. On this path the caller's bearer travels with
	// it, because a declared upstream authenticates a PERSON's request by it and
	// no capability minted from a browser session replaces it yet (SA-F-BEARER,
	// open on this path; see removePersonsSessionCredential in gateway.go). The
	// bearer-less arm above forwards none. The caller's COOKIE does not travel,
	// and a resource-bound token cannot be replayed off its own solution, so what
	// an upstream holds is the person's host session and nothing wider.
	//
	// A module data endpoint and its admission read must consume the
	// same per-org/per-IP budget an equivalent catalog route does, or /v1/<alias>/*
	// would be an unmetered proxy an authenticated caller could flood past the org
	// budget.
	entry := &RouteEntry{Service: moduleServicePrefix + alias, Protected: true}
	org, viewer := r.Header.Get("X-Org-Id"), effectiveViewer(r)
	if org == "" || viewer == "" {
		httpError(w, http.StatusForbidden, "no organization in this session")
		return true
	}
	g.rateLimitThenServe(w, r, entry, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admission, installation := g.admitViewerSolutionInstallation(r.Context(), org, viewer, routing)
		switch admission {
		case viewerSolutionNotEntitled:
			httpError(w, http.StatusForbidden, "module is not installed for this organization")
			return
		case viewerSolutionUndecidable:
			httpError(w, http.StatusServiceUnavailable, "module installation authority unavailable")
			return
		}
		// Which installation admitted this viewer, forwarded for the same reason
		// it is forwarded to a solution: the consent is a fact about the request,
		// and the upstream that serves it is the one that needs it. Gateway-owned
		// and on untrustedAuthHeaders, so a caller's own value is already gone.
		if installation != "" {
			r.Header.Set(installationConfinementHeader, installation)
		}
		g.proxyTo(w, r, routing.Upstream, entry)
	}))
	return true
}

// declaredModuleKind reports whether a record's declaration says "module".
//
// The kind is read from the declaration, never from the record's shape. A
// publisher prefix or an alias convention would be a guess at the one question
// that separates a surface with per-viewer admission from one without, and
// UNSPECIFIED — what a reader decodes from a writer that did not set the field —
// is not a kind: it answers false here and false in the solution check, so such a
// record is served by neither surface rather than by whichever needs less
// authority.
func declaredModuleKind(record *accountsv1.SolutionRegistration) bool {
	return record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE
}

// declaredSolutionKind is the same read for the solution surface.
func declaredSolutionKind(record *accountsv1.SolutionRegistration) bool {
	return record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION
}
