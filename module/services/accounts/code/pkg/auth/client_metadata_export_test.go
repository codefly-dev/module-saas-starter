package auth

import (
	"context"
	"net/http"
	"syscall"
	"time"
)

// Test hooks for the metadata resolver, in package auth so the external
// auth_test package can reach them without any of this being production API.
//
// There are exactly two, and each exists because the thing it replaces cannot
// be exercised otherwise: the SSRF dial guard refuses every loopback address,
// which is every address a test server listens on, and the transport pins TLS
// to the public roots, which no test certificate is in.

// SetMetadataDialGuardForTest replaces the dial guard and returns a function
// restoring it.
func SetMetadataDialGuardForTest(guard func(string, string, syscall.RawConn) error) func() {
	previous := metadataDialGuard
	metadataDialGuard = func(string) DialControl { return guard }
	return func() { metadataDialGuard = previous }
}

// MetadataDialGuardForTest exposes the production guard for a dial to
// requested ("host:port") so a test can assert what it refuses without opening
// a connection.
func MetadataDialGuardForTest(requested string) func(string, string, syscall.RawConn) error {
	return metadataDialGuard(requested)
}

// WrapMetadataDialGuardForTest wraps the production guard's hook for each dial
// with wrap (a recorder that stops the dial, for example) and returns a
// function restoring it.
func WrapMetadataDialGuardForTest(wrap func(DialControl) DialControl) func() {
	previous := metadataDialGuard
	metadataDialGuard = func(requested string) DialControl { return wrap(previous(requested)) }
	return func() { metadataDialGuard = previous }
}

// SetMetadataResolverForTest replaces the resolver a metadata fetch dial
// consults and returns a function restoring it.
func SetMetadataResolverForTest(resolver Resolver) func() {
	previous := metadataResolver
	metadataResolver = resolver
	return func() { metadataResolver = previous }
}

// FetchMetadataDocumentForTest runs the production HTTP fetch of one document
// URL, with its real transport, and returns only the error.
func FetchMetadataDocumentForTest(ctx context.Context, documentURL string) error {
	_, _, err := newHTTPMetadataFetcher().Fetch(ctx, documentURL)
	return err
}

// SetTransportForTest points this resolver's HTTP fetcher at a test server's
// transport. It panics on a resolver built over any other fetcher, which is a
// test wiring mistake rather than a condition to tolerate.
func (r *ClientMetadataResolver) SetTransportForTest(transport http.RoundTripper) {
	r.fetcher.(*httpMetadataFetcher).client.Transport = transport
}

// CachedMetadataTTLForTest exposes how long a document's own Cache-Control
// would be honoured, bounds applied.
func CachedMetadataTTLForTest(cacheControl string) time.Duration {
	return metadataCacheTTL(cacheControl)
}
