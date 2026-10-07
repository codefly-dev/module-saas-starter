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
// This copies the solution shape rather than inventing a second one: the same
// cache, the same single carried resolution, the same upstream policy, the same
// tombstone semantics, the same 120 s revocation bound. What it deliberately does
// NOT copy is per-viewer admission. A solution route resolves the viewer's
// installation of the record's target; a module is part of the composition, not
// something an organisation installs, so the ordinary authenticated pipeline is
// the whole gate — exactly as main's federated route was.
//
// That asymmetry is why the registry must SAY which kind a record is, and why
// neither surface guesses: routing a solution's upstream here would put it behind
// no installation check at all.

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

	// THE ROUTE IS RESOLVED, SO THE AUDIENCE IS KNOWN. A module route's target is
	// the module capability surface, so that is what a capability presented here
	// must name — the same derivation as a solution route, with the audience the
	// host's vocabulary gives this kind.
	if g.rejectInvalidWorkContext(w, r, moduleCapabilitiesAudience) {
		return true
	}

	// Same identity discipline as every protected route: drop caller-supplied
	// identity, run ext_authz, require a valid credential. A bearer-less call is
	// denied here, so a declared module adds a proxy target and never widens the
	// authenticated surface.
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

	// Forward the full /v1/<alias>/... path unchanged: the module owns and serves
	// its own /v1/<alias> surface. The caller's bearer is preserved so the module
	// can call back through the gateway on the user's behalf, exactly as solution
	// passthrough does.
	//
	// rateLimitThenProxy, not proxyTo: a module data endpoint must consume the
	// same per-org/per-IP budget an equivalent catalog route does, or /v1/<alias>/*
	// would be an unmetered proxy an authenticated caller could flood past the org
	// budget.
	entry := &RouteEntry{Service: moduleServicePrefix + alias, Protected: true}
	g.rateLimitThenProxy(w, r, routing.Upstream, entry)
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
