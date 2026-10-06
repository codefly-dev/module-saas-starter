package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

type verifiedPublicOriginKey struct{}

// configuredPublicOrigin is the origin an operator pinned for this deployment,
// installed once at startup. The empty string means none is pinned, which only a
// local runtime reaches: boot requires one outside local development.
var configuredPublicOrigin string

// SetConfiguredPublicOrigin installs the operator-configured public origin. Call
// it once from startup, before anything serves.
//
// The value is stored CANONICALIZED, through the same function the forwarded origin
// goes through, so the two sides of the comparison cannot spell one origin two ways.
// Storing it raw is what made `https://app.example:443` refuse the equivalent origin
// the frontend forwards. A value that does not canonicalize is stored trimmed, so the
// comparison refuses it rather than silently matching something.
func SetConfiguredPublicOrigin(origin string) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if canonical, err := CanonicalPublicOrigin(trimmed); err == nil {
		configuredPublicOrigin = canonical
		return
	}
	configuredPublicOrigin = trimmed
}

// ConfiguredPublicOrigin returns the pinned origin, if there is one.
func ConfiguredPublicOrigin() (string, bool) {
	return configuredPublicOrigin, configuredPublicOrigin != ""
}

// ErrPublicOriginNotConfigured is returned when a forwarded origin does not match
// the one this deployment pinned.
var ErrPublicOriginNotConfigured = errors.New(
	"public origin does not match the configured application base URL")

// CanonicalPublicOrigin validates an exact browser origin: absolute, HTTP(S),
// HTTPS unless loopback, and nothing but scheme and authority. It says a value is
// WELL FORMED, never that it is trusted — WithVerifiedPublicOrigin is what decides
// that.
func CanonicalPublicOrigin(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("public origin must be an absolute HTTP(S) origin")
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("public origin must not contain credentials, path, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host, err := canonicalPublicOriginHost(parsed.Hostname())
	if err != nil {
		return "", err
	}
	switch scheme {
	case "https":
	case "http":
		if !isPublicOriginLoopback(host) {
			return "", fmt.Errorf("non-loopback public origin must use HTTPS")
		}
	default:
		return "", fmt.Errorf("public origin must use HTTP(S)")
	}
	port, err := canonicalPublicOriginPort(parsed.Port(), scheme)
	if err != nil {
		return "", err
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 literal keeps its brackets
	}
	return scheme + "://" + host + port, nil
}

// canonicalPublicOriginHost renders a host the one way the browser renders it, so
// two spellings of one origin cannot compare unequal (SP-GW-08, R1019-N12).
//
// The frontend half of this comparison is `new URL(...).origin`, the WHATWG parser.
// Go's net/url is laxer than that parser in four ways that each produced a
// browser-equivalent spelling this function used to refuse: it preserves host case,
// leaves an IPv6 literal in whatever form it was written, leaves a Unicode hostname
// un-encoded, and treats a host-less authority as a host. Each is settled here —
// lowercase, the compressed IPv6 form, IDNA/punycode, and a refusal — rather than at
// the comparison, because a comparison can only reject what reaches it in one shape.
func canonicalPublicOriginHost(hostname string) (string, error) {
	if hostname == "" {
		// `https://:443` parses in Go with an empty hostname. A browser rejects it,
		// so there is no origin it could be equivalent to.
		return "", fmt.Errorf("public origin must name a host")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		// The compressed form: an expanded literal and its compression are one address,
		// and net.IP.String() picks the same representation the browser does.
		return ip.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(hostname)
	if err != nil {
		return "", fmt.Errorf("public origin host is not a usable hostname")
	}
	return strings.ToLower(ascii), nil
}

// canonicalPublicOriginPort renders the port as the ":NNN" suffix of a canonical
// origin, or "" when the origin carries the scheme's default.
//
// An explicit DEFAULT port is the same origin as none, and the two sides spell it
// differently: the browser and the frontend both drop it, while a configured value may
// carry it. A leading-zero port, and an empty explicit port, are likewise the same
// origin the browser renders without them. Anything outside the one-to-65535 range is
// not a port at all, and the browser refuses the whole URL rather than ignoring it.
func canonicalPublicOriginPort(port, scheme string) (string, error) {
	if port == "" {
		return "", nil // including `https://app.example:`, which Go reports as no port
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("public origin port must be between 1 and 65535")
	}
	if (scheme == "https" && number == 443) || (scheme == "http" && number == 80) {
		return "", nil
	}
	return ":" + strconv.Itoa(number), nil // decimal, so a leading zero cannot survive
}

// WithVerifiedPublicOrigin records the origin only after the gateway credential
// has been authenticated by an adapter — AND only when it is the origin this
// deployment pinned.
//
// The invariant: the origin this service treats as its verified public origin — the
// one that binds a sign-in redirect, an authenticator's relying party and an
// emailed link — is fixed by operator configuration (SP-GW-08). A forwarded value
// is honoured only where it equals the configured one, so the configuration is the
// authority and a forwarding hop cannot substitute for it. This check is
// independent of any hop's own correctness, which is why it lives here as well.
//
// Where nothing is pinned the candidate stands on the credential alone, which only
// a local runtime reaches: boot requires a pinned origin outside local development
// (requireApplicationBaseURL).
func WithVerifiedPublicOrigin(ctx context.Context, candidate string) (context.Context, error) {
	origin, err := CanonicalPublicOrigin(candidate)
	if err != nil {
		return ctx, err
	}
	if configured, ok := ConfiguredPublicOrigin(); ok && !strings.EqualFold(origin, configured) {
		return ctx, ErrPublicOriginNotConfigured
	}
	return context.WithValue(ctx, verifiedPublicOriginKey{}, origin), nil
}

func VerifiedPublicOrigin(ctx context.Context) (string, bool) {
	origin, ok := ctx.Value(verifiedPublicOriginKey{}).(string)
	return origin, ok && origin != ""
}

func isPublicOriginLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
