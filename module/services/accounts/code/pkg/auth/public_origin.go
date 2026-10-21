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

// publicOriginHostProfile renders a host the way the browser's URL parser does.
//
// idna.Lookup — the obvious choice, and what this used first — additionally enforces
// DNS hostname policy, which refuses an UNDERSCORE in a label. Browsers accept one,
// and internal and staging hostnames routinely carry one, so Lookup refused origins a
// deployment legitimately serves while the frontend accepted them (R1019-N12/f1).
// StrictDomainName(false) drops that policy and keeps the mapping.
//
// idna.Punycode is NOT the alternative, though it accepts more: it does raw conversion
// with no case folding, so "BÜCHER.example" encodes to "xn--BCHER-2pa.example" while
// "bücher.example" encodes to "xn--bcher-kva.example" — two spellings of one origin
// that would never compare equal, and lowercasing afterwards cannot merge them because
// the punycode payloads already differ. MapForLookup case-folds BEFORE encoding, which
// is why it is the mapping used here and why no ToLower is needed after it.
//
// VerifyDNSLength(false) because a length limit is the resolver's to enforce, not this
// comparison's: refusing an over-long host here would reject it at sign-in rather than
// where it actually fails.
var publicOriginHostProfile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.StrictDomainName(false),
	idna.VerifyDNSLength(false),
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
func SetConfiguredPublicOrigin(origin string) error {
	trimmed := strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if trimmed == "" {
		configuredPublicOrigin = ""
		return nil
	}
	canonical, err := CanonicalPublicOrigin(trimmed)
	if err != nil {
		// Storing the raw value on failure looked like the conservative branch and was
		// the opposite: a value that cannot canonicalize can never equal a canonicalized
		// candidate, so every origin comparison failed for the life of the process while
		// boot reported success (R1019-N12/f2). The caller refuses instead.
		return fmt.Errorf("configured public origin is unusable: %w", err)
	}
	configuredPublicOrigin = canonical
	return nil
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
	if numericHostSpelling(hostname) {
		// A host the WHATWG parser reads as an IPv4 ADDRESS but spells differently
		// from this side. `https://2130706433`, `https://0x7f000001` and
		// `https://127.1` are all 127.0.0.1 to a browser and to the frontend's
		// `new URL(...)`, which rewrites them to the dotted quad; Go's net/url
		// preserves whatever was written. Both sides would then be configured with
		// one value and compare two, so every same-origin check fails and sign-in
		// refuses — a configuration that cannot work, accepted at boot.
		//
		// Refused rather than rewritten. Rewriting would mean reimplementing that
		// parser's integer-host rules in Go and keeping them in step forever, for a
		// spelling no deployment needs: a dotted quad says the same thing and both
		// sides already agree on it.
		return "", fmt.Errorf(
			"public origin host %q is a numeric spelling of an address, which this "+
				"host and the browser's URL parser render differently; write it as a "+
				"dotted quad (or a hostname)", hostname)
	}
	ascii, err := publicOriginHostProfile.ToASCII(hostname)
	if err != nil {
		return "", fmt.Errorf("public origin host is not a usable hostname")
	}
	return ascii, nil
}

// numericHostSpelling reports whether a hostname that is NOT already a canonical
// IP literal would be read as an IPv4 address by the WHATWG URL parser.
//
// That parser's trigger is the last label: if it parses as a number — decimal,
// hex `0x…`, or octal with a leading zero — the whole host is an address and is
// re-rendered as a dotted quad. net.ParseIP has already accepted every spelling
// the two sides agree on before this runs, so anything reaching here with a
// numeric final label is a spelling only one side would keep.
func numericHostSpelling(hostname string) bool {
	labels := strings.Split(hostname, ".")
	last := labels[len(labels)-1]
	if last == "" {
		// A trailing dot: the label before it decides.
		if len(labels) < 2 {
			return false
		}
		last = labels[len(labels)-2]
	}
	if last == "" {
		return false
	}
	if lower := strings.ToLower(last); strings.HasPrefix(lower, "0x") {
		_, err := strconv.ParseUint(lower[2:], 16, 64)
		return err == nil && len(lower) > 2
	}
	_, err := strconv.ParseUint(last, 10, 64)
	return err == nil
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
//
// A refusal names which check failed and the values it compared — the forwarded
// candidate and, on a mismatch, both canonical origins. An origin is not a secret,
// and the refuser's log is the one place an operator can learn that the frontend
// presented its cluster-internal address rather than the configured one. Callers
// log it and keep their caller-facing answer generic.
func WithVerifiedPublicOrigin(ctx context.Context, candidate string) (context.Context, error) {
	origin, err := CanonicalPublicOrigin(candidate)
	if err != nil {
		return ctx, fmt.Errorf("forwarded public origin %q is not a usable origin: %w", candidate, err)
	}
	if configured, ok := ConfiguredPublicOrigin(); ok && !strings.EqualFold(origin, configured) {
		return ctx, fmt.Errorf("%w: forwarded %q, configured application/APP_BASE_URL %q",
			ErrPublicOriginNotConfigured, origin, configured)
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
