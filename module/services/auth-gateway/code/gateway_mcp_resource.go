package main

import (
	"net/http"
	"net/url"
	"strings"
)

// The solution MCP endpoint as an OAuth protected resource (issue #1003).
//
// A solution's MCP surface is `/solutions/<id>/mcp`, served by the solution's
// own runtime and fronted by this gateway. The gateway owns the authentication
// half of it and nothing else: there is no MCP code here, no tool list, no
// protocol handling. What it owns is the three OAuth facts a client needs and
// the one rule only this process can enforce:
//
//  1. An unauthenticated request is answered 401 with `WWW-Authenticate`
//     naming where the resource describes itself (RFC 6750 §3, RFC 9728 §5.1).
//  2. That description — RFC 9728 protected-resource metadata — is served here,
//     because the gateway is the component that decides what a token must look
//     like to get in. A document published by anything else would be a claim
//     about this code made somewhere else.
//  3. A token carrying a resource audience is admitted only at the solution it
//     names. That is the whole point of RFC 8707: a token the person approved
//     for one solution must be refused at another, so one solution's
//     runtime cannot replay it against another's.
//
// The identity headers a solution receives are unchanged by all of this: the
// same ext_authz Check runs, stamps the same `x-user-id`/`x-org-id`/
// `x-session-id`/`x-credential-kind: session`, and the runtime's auth is
// exactly what it was.

const (
	// solutionMCPSegment is the sub-path a solution serves Streamable HTTP on.
	// One segment, no children: an MCP server is a single endpoint, and
	// matching a prefix here would extend the resource binding to paths no
	// resource indicator describes.
	solutionMCPSegment = "mcp"
	// solutionProxyBase is the host frontend's public route to a solution's
	// backend, and on a deployed cell the ONLY one: it forwards the caller's
	// bearer to this gateway and already serves the solution's `.well-known`
	// anonymously. `/solutions/<id>/*` is this gateway's internal surface — not
	// public, and on the public origin a page of the frontend that redirects to
	// login — so a challenge naming it sends a client to a login page instead
	// of a JSON document.
	//
	// Spelled here because this is Go and the frontend is TypeScript
	// (registry.ts solutionProxyBase); TestTheChallengeNamesTheFrontendProxyBase
	// holds the two together.
	solutionProxyBase = "/api/solutions/"
	solutionProxyMid  = "/proxy"
)

// isSolutionMCPPath reports whether a solution sub-path (the suffix after
// `/solutions/<id>/`, with no leading slash) is that solution's MCP endpoint.
func isSolutionMCPPath(subPath string) bool {
	return subPath == solutionMCPSegment
}

// isSolutionToolRequestPath reports whether a FULL request path addresses a
// solution's MCP endpoint — the surface the security posture treats as an OAuth
// protected resource in its own right (SP-SOL-07).
//
// It is deliberately narrower than "any solution path". A solution's other
// routes are the product's own API surface, reached by a signed-in person's
// session through the host's pages; requiring a resource-bound token there
// would refuse the product. The tool endpoint is the one a third-party client
// connects to, and the one whose metadata this host publishes, so it is the one
// that must demand a token issued for itself.
func isSolutionToolRequestPath(path string) bool {
	pathOnly, _, _ := strings.Cut(path, "?")
	rest, ok := strings.CutPrefix(pathOnly, solutionPrefix)
	if !ok {
		return false
	}
	_, subPath, found := strings.Cut(rest, "/")
	return found && isSolutionMCPPath(subPath)
}

// publicBaseURL is the operator-trusted public origin of this host, read from
// the `application` configuration group — the same APP_BASE_URL accounts uses
// for the links it mints. It is empty when the deployment has not pinned one.
//
// When it is set, a resource audience must name that exact origin, so a token
// minted against some other host's spelling of this solution is refused. When
// it is not (local development, where no runtime port is pinned), the origin
// cannot be checked and only the solution the resource names is — which is
// still what stops one solution's token reaching another, and is the property
// the gateway exists to enforce.
func publicBaseURL() string {
	return strings.TrimSuffix(strings.TrimSpace(workspaceEnv("application", "APP_BASE_URL")), "/")
}

// resourceAudienceAdmits reports whether a token's resource audience permits a
// request to solutionID. The audience must be a URL naming that solution's MCP
// endpoint, at this host's configured origin when one is configured.
//
// A token with no resource audience never reaches here: the caller treats an
// unbound session token as admissible, which is what keeps the browser's own
// session and the first registered client working at a solution's surface.
func resourceAudienceAdmits(resource, solutionID, publicBase string) bool {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	// The resource names the PUBLIC proxy path a client can reach; this request
	// arrived on this gateway's own internal path. Both carry the solution id,
	// which is what the comparison turns on.
	if parsed.EscapedPath() != solutionProxyBase+solutionID+solutionProxyMid+"/"+solutionMCPSegment {
		return false
	}
	if publicBase == "" {
		return true
	}
	origin := strings.ToLower(parsed.Scheme) + "://" + parsed.Host
	return strings.EqualFold(origin, publicBase)
}

// solutionIDFromPath extracts the solution a request path addresses, for the
// audience check. It returns ok=false for every path that is not a solution
// path, which is how a resource-bound token keeps reaching the host's own API:
// the host is where the resource resolves the caller's authority, so the token
// names it too (see the minter's two-audience comment).
func solutionIDFromPath(path string) (string, bool) {
	pathOnly, _, _ := strings.Cut(path, "?")
	rest, ok := strings.CutPrefix(pathOnly, solutionPrefix)
	if !ok {
		return "", false
	}
	id, _, _ := strings.Cut(rest, "/")
	if id == "" || !solutionIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// solutionResourceMetadataPath is the URL the 401 challenge names: the
// solution's own well-known under the host frontend's PROXY base.
//
// Issue #1003's comment of 2026-10-04 asked for a URL derivable from the route,
// pointing at the `.well-known` GET that is already public. The first attempt
// named this gateway's own `/solutions/<id>/...` path, which is not reachable
// from outside — following that challenge on a real cell lands on a login
// redirect, so discovery could never reach the authorization server. This is
// the path a client can actually fetch, and the one that already answers 200
// anonymously: the gateway proxies a solution's `.well-known` GET
// unauthenticated and its runtime serves the RFC 9728 document there, naming
// this host as the authorization server.
//
// The host deliberately serves no second copy of that document. Two documents
// for one resource are two places to disagree, and a client follows whichever
// URL the challenge gives it.
func solutionResourceMetadataPath(solutionID string) string {
	return solutionProxyBase + solutionID + solutionProxyMid +
		"/.well-known/oauth-protected-resource"
}

// stampResourceChallenge puts the RFC 6750 / RFC 9728 challenge on a denied
// request to a solution's surface. It is what starts an MCP client's discovery
// chain: the client reads `resource_metadata`, fetches the document, finds this
// host as the authorization server, and runs the authorization-code flow.
// Without this header a client has nowhere to begin and reports only
// "unauthorized".
//
// Stamped for EVERY protected `/solutions/<id>/*` path, not only the exact
// `/mcp`. The gateway is where such a request is denied — it strips identity,
// runs ext_authz and answers 401 itself, never proxying — so a challenge the
// runtime would have sent can only reach a direct-to-port caller. A 401 here
// without it is a dead end, and that was true for `/solutions/<id>/mcp/` (a
// trailing slash) and for every other solution path. Required by issue #1003's
// comment of 2026-10-04.
func stampResourceChallenge(w http.ResponseWriter, solutionID string) {
	base := publicBaseURL()
	metadata := solutionResourceMetadataPath(solutionID)
	if base != "" {
		metadata = base + metadata
	}
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", resource_metadata="`+metadata+`"`)
}

// legacyTokenIssuer is the literal `iss` accounts minted before it published
// RFC 8414 metadata. Spelled here rather than imported: the gateway is its own
// Go module and may not depend on accounts. TestTokenIssuerMatchesAccounts
// holds the two together.
const legacyTokenIssuer = "saas-starter"

// gatewayTokenIssuer is the `iss` accounts mints today — the configured public
// base URL, or the literal when none is configured. Derived from the same
// APP_BASE_URL both sides read, so the two cannot be configured apart.
func gatewayTokenIssuer() string {
	if base := publicBaseURL(); base != "" {
		return base
	}
	return legacyTokenIssuer
}

// gatewayAcceptedIssuers is what verification admits besides the minted one:
// the literal, while tokens minted under it are still unexpired. A deployment
// that never moved mints the literal anyway, so the extra entry is a no-op
// there rather than a second trusted issuer.
func gatewayAcceptedIssuers() []string {
	return []string{legacyTokenIssuer}
}
