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
func SetConfiguredPublicOrigin(origin string) {
	configuredPublicOrigin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
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
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !isPublicOriginLoopback(parsed.Hostname()) {
			return "", fmt.Errorf("non-loopback public origin must use HTTPS")
		}
	default:
		return "", fmt.Errorf("public origin must use HTTP(S)")
	}
	return strings.TrimSuffix(candidate, "/"), nil
}

// WithVerifiedPublicOrigin records the origin only after the gateway credential
// has been authenticated by an adapter — AND only when it is the origin this
// deployment pinned.
//
// The credential check alone was not enough, and that is the whole of the defect
// it closes. The frontend held the internal token legitimately, and derived the
// origin it stamped from the caller's own `X-Forwarded-Host` whenever its rendered
// endpoint was a loopback placeholder, which is what a render produces. So an
// authenticated hop forwarded a caller's choice, and this recorded it as VERIFIED:
// an OAuth redirect, the authenticator relying-party origin and the links mailed
// out were then bound to a host the caller named.
//
// Comparing against the pinned origin makes that unreachable regardless of what
// any hop forwards — a second, independent barrier to the frontend's own fix, and
// the one that holds for a hop this service does not own. Where nothing is pinned
// the candidate stands on the credential alone, which only a local runtime reaches:
// boot requires a pinned origin outside local development.
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
