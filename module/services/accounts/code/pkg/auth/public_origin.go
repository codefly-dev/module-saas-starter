package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
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
	switch scheme {
	case "https":
	case "http":
		if !isPublicOriginLoopback(parsed.Hostname()) {
			return "", fmt.Errorf("non-loopback public origin must use HTTPS")
		}
	default:
		return "", fmt.Errorf("public origin must use HTTP(S)")
	}
	// An explicit DEFAULT port is the same origin as none, and the two sides of the
	// comparison spell it differently: the browser and the frontend both drop it
	// (URL.origin does), while a configured value may carry it. Retaining it here
	// made an operator's `https://app.example:443` refuse the equivalent origin the
	// frontend forwards. Dropping it is the same rule both sides then follow.
	host := parsed.Host
	if port := parsed.Port(); port != "" &&
		((scheme == "https" && port == "443") || (scheme == "http" && port == "80")) {
		host = parsed.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]" // an IPv6 literal keeps its brackets
		}
	}
	return scheme + "://" + host, nil
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
