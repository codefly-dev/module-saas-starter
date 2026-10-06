package auth

import (
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
	metadataDialGuard = guard
	return func() { metadataDialGuard = previous }
}

// MetadataDialGuardForTest exposes the production guard so a test can assert
// what it refuses without opening a connection.
func MetadataDialGuardForTest() func(string, string, syscall.RawConn) error {
	return metadataDialGuard
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
