package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUntrustedHeaders_SupersetOfStampedHeaders is the sidecar half of the
// header-lockstep gate. The gateway strips untrustedAuthHeaders from every
// inbound request and then stamps canonicalUpstreamAuthHeaders from the verified
// principal. If a header the gateway stamps is not also stripped, a caller could
// set it on a route that stamps only a subset (a public route without a token,
// or an api-key check that omits it), and the spoofed value would reach the
// upstream unreplaced. The strip set must therefore be a superset of the stamped
// set, so every canonical header is scrubbed before it is (re)stamped.
func TestUntrustedHeaders_SupersetOfStampedHeaders(t *testing.T) {
	strip := make(map[string]bool, len(untrustedAuthHeaders))
	for _, h := range untrustedAuthHeaders {
		require.Equal(t, strings.ToLower(h), h, "untrustedAuthHeaders must be lowercase for case-insensitive stripping")
		strip[h] = true
	}

	for _, h := range canonicalUpstreamAuthHeaders {
		require.True(t, strip[strings.ToLower(h)],
			"stamped upstream header %q must also be in untrustedAuthHeaders so a spoofed inbound value is stripped before it is stamped", h)
	}
}
