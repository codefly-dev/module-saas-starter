package auth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// RFC 8707 resource indicators.
//
// An MCP client names the thing it wants a token for — a solution's MCP
// endpoint — and the token it receives carries that URL as an audience. The
// point of the indicator is that the token is then useless at a different
// resource: a confused deputy cannot replay one solution's token against another
// solution, because the audience says which one it was minted for.
//
// The host accepts exactly one shape of resource today: a solution's MCP
// endpoint at this host's own public origin. That is deliberately narrow. A
// resource the host cannot describe is a resource the gateway cannot enforce,
// and an audience nothing checks is worse than no audience at all.

// ErrResourceRejected is every reason a resource indicator is refused. Like
// ErrClientAuthorizationRejected it is a single sentinel: the authorize
// endpoint answers an unauthenticated browser, and a distinguishable refusal
// would report which solutions exist.
var ErrResourceRejected = errors.New("resource indicator rejected")

// solutionMCPSuffix is the one resource path the host mints audiences for. It
// is the gateway's own `/solutions/<id>/<path>` surface with `mcp` as the path,
// which is where a solution's runtime serves Streamable HTTP.
const solutionMCPSuffix = "/mcp"

// solutionResourcePrefix is the path prefix every resource indicator carries.
const solutionResourcePrefix = "/solutions/"

// ResourceIndicator is a validated RFC 8707 `resource` value: the exact string
// that becomes the token's audience, plus the parts the gateway and the consent
// screen need without re-parsing it.
type ResourceIndicator struct {
	// Value is the canonical indicator, byte-for-byte what goes in `aud` and
	// what a verifier compares against. RFC 8707 requires the indicator be used
	// as given, so it is never re-rendered from the parts below.
	Value string
	// Origin is the scheme://host[:port] the indicator names.
	Origin string
	// SolutionID is the solution whose MCP endpoint this is.
	SolutionID string
}

// SolutionMCPResource builds the indicator for one solution at one origin. It
// is the single place the string is composed, so the authorization server, the
// published metadata, and the gateway's expectation cannot drift apart.
func SolutionMCPResource(origin, solutionID string) string {
	return strings.TrimSuffix(origin, "/") + solutionResourcePrefix + solutionID + solutionMCPSuffix
}

// ParseResourceIndicator validates a client-supplied `resource` parameter
// structurally: an absolute https URI (http only for a loopback development
// host), no query, no fragment, no userinfo, naming a solution's MCP endpoint.
//
// RFC 8707 §2 requires the indicator be an absolute URI without a fragment and
// says a query component SHOULD NOT be used; both are refused here rather than
// normalised away, because the value is compared by equality downstream and a
// tolerated variant would be an audience the gateway never matches.
//
// It deliberately does NOT check that the solution is registered. Registration
// is live state that comes and goes on a lease, and a token naming a solution
// that is not serving is harmless — the gateway answers 503 for it. Refusing
// here would instead make the authorize endpoint an existence oracle for every
// solution in the deployment, to an unauthenticated caller.
func ParseResourceIndicator(candidate string) (ResourceIndicator, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || len(candidate) > 2048 {
		return ResourceIndicator{}, ErrResourceRejected
	}
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return ResourceIndicator{}, ErrResourceRejected
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Opaque != "" {
		return ResourceIndicator{}, ErrResourceRejected
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return ResourceIndicator{}, ErrResourceRejected
		}
	default:
		return ResourceIndicator{}, ErrResourceRejected
	}
	// The escaped form is compared, not the decoded one: `parsed.Path` would
	// read `/solutions/a%2Fb/mcp` as a two-segment id, and the audience string
	// the client sent is the escaped one.
	path := parsed.EscapedPath()
	rest, ok := strings.CutPrefix(path, solutionResourcePrefix)
	if !ok {
		return ResourceIndicator{}, ErrResourceRejected
	}
	solutionID, ok := strings.CutSuffix(rest, solutionMCPSuffix)
	if !ok || !ValidSolutionID(solutionID) {
		return ResourceIndicator{}, ErrResourceRejected
	}
	origin := strings.ToLower(parsed.Scheme) + "://" + parsed.Host
	return ResourceIndicator{
		Value:      candidate,
		Origin:     origin,
		SolutionID: solutionID,
	}, nil
}

// RequireResourceAtOrigin parses candidate and requires it to name a resource
// at origin. The authorization server pins the origin to its own public base so
// it cannot be talked into minting an audience for somebody else's host — which
// is the whole confused-deputy risk resource indicators exist to close.
func RequireResourceAtOrigin(candidate, origin string) (ResourceIndicator, error) {
	resource, err := ParseResourceIndicator(candidate)
	if err != nil {
		return ResourceIndicator{}, err
	}
	expected, err := CanonicalPublicOrigin(origin)
	if err != nil {
		return ResourceIndicator{}, fmt.Errorf("%w: no trusted public origin", ErrResourceRejected)
	}
	if !strings.EqualFold(resource.Origin, expected) {
		return ResourceIndicator{}, ErrResourceRejected
	}
	return resource, nil
}

// ValidSolutionID mirrors the identity rule the solution registry and the
// gateway's own path matcher enforce: one lowercase segment usable both as a
// path element and as a routing key. Spelled out here rather than imported,
// because pkg/auth may not depend on the solution registry.
func ValidSolutionID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for index, character := range id {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case (character == '-' || character == '_') && index > 0 && index < len(id)-1:
		default:
			return false
		}
	}
	return true
}
