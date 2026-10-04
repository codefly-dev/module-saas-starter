package main

import (
	"encoding/json"
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
	// protectedResourceMetadataPrefix is where RFC 9728 §3.1 puts a resource's
	// metadata: the well-known name, then the resource's own path.
	protectedResourceMetadataPrefix = "/.well-known/oauth-protected-resource"
)

// isSolutionMCPPath reports whether a solution sub-path (the suffix after
// `/solutions/<id>/`, with no leading slash) is that solution's MCP endpoint.
func isSolutionMCPPath(subPath string) bool {
	return subPath == solutionMCPSegment
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
	if parsed.EscapedPath() != solutionPrefix+solutionID+"/"+solutionMCPSegment {
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

// protectedResourceMetadata is the RFC 9728 document for one solution's MCP
// endpoint.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// mcpResourceMetadataPath is where a client looks for the metadata of the
// resource `<base>/solutions/<id>/mcp`, per RFC 9728 §3.1: the well-known
// prefix followed by the resource's own path.
func mcpResourceMetadataPath(solutionID string) string {
	return protectedResourceMetadataPrefix + solutionPrefix + solutionID + "/" + solutionMCPSegment
}

// handleProtectedResourceMetadata serves the RFC 9728 document. It returns true
// when it has handled the request.
//
// Unauthenticated by design: this is the document that tells a client how to
// authenticate, so requiring authentication to read it would close the only
// loop that gets a first-time client a token. It carries no tenant data — the
// resource's URL, this host as its authorization server, and the one scope the
// authorization server publishes.
func (g *Gateway) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, protectedResourceMetadataPrefix) {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
	// Identity headers are stripped exactly as they are on every other
	// unauthenticated surface here: nothing downstream reads them on this path,
	// and leaving a caller-supplied value in place on any route is how one
	// eventually survives into a route that does.
	stripAllIdentityHeaders(r)

	rest := strings.TrimPrefix(r.URL.Path, protectedResourceMetadataPrefix)
	solutionID, isMCP := solutionIDFromMetadataPath(rest)
	if !isMCP {
		httpError(w, http.StatusNotFound, "no such protected resource")
		return true
	}
	base := publicBaseURL()
	if base == "" {
		// Every URL in the document would be a guess, and a client that
		// fetched it would send its authorization request to whatever it named.
		// Say so rather than publishing one.
		httpError(w, http.StatusServiceUnavailable, "this host has no configured public address")
		return true
	}
	// Registration is deliberately not consulted. The document describes how to
	// authenticate for a resource, which does not change while a solution's
	// lease is lapsed, and answering 404 here for an unregistered id would make
	// an unauthenticated endpoint an inventory of the deployment's solutions.
	// A token for a solution that is not serving still meets a 503 at the
	// resource itself, which is the honest answer about availability.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protectedResourceMetadata{
		Resource:               base + solutionPrefix + solutionID + "/" + solutionMCPSegment,
		AuthorizationServers:   []string{base},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{publishedOAuthScope},
	})
	return true
}

// publishedOAuthScope mirrors accounts' supportedOAuthScope. The two must say
// the same thing — a resource that advertises a scope its authorization server
// does not issue sends clients to ask for something they cannot get — and
// TestProtectedResourceScopeMatchesTheAuthorizationServer holds them together.
const publishedOAuthScope = "offline_access"

// solutionIDFromMetadataPath reads `/solutions/<id>/mcp` out of the suffix
// after the well-known prefix. Nothing else is a resource this host describes.
func solutionIDFromMetadataPath(rest string) (string, bool) {
	trimmed, ok := strings.CutPrefix(rest, solutionPrefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(trimmed, "/"+solutionMCPSegment)
	if !ok || !solutionIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// stampResourceChallenge puts the RFC 6750 / RFC 9728 challenge on a 401 from a
// solution's MCP endpoint. It is what starts an MCP client's discovery chain:
// the client reads `resource_metadata`, fetches the document, finds this host
// as the authorization server, and runs the authorization-code flow. Without
// this header a client has nowhere to begin and reports only "unauthorized".
func stampResourceChallenge(w http.ResponseWriter, solutionID string) {
	base := publicBaseURL()
	metadata := mcpResourceMetadataPath(solutionID)
	if base != "" {
		metadata = base + metadata
	}
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", resource_metadata="`+metadata+`"`)
}
