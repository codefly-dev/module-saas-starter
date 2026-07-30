package auth

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type verifiedPublicOriginKey struct{}

// CanonicalPublicOrigin validates an exact browser origin. The trusted gateway
// uses this for the Codefly SDK-injected public endpoint before placing it in a
// request context; business code never derives security-sensitive URLs from
// caller-controlled Host or forwarding headers.
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
// has been authenticated by an adapter.
func WithVerifiedPublicOrigin(ctx context.Context, candidate string) (context.Context, error) {
	origin, err := CanonicalPublicOrigin(candidate)
	if err != nil {
		return ctx, err
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
