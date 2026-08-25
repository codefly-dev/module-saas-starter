package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The anonymous endpoints are unthrottled ONLY when both guards are off: abuse
// protection disabled AND no rate limiter (no Redis). Either one alone still
// leaves a backstop, so the loud "UNPROTECTED" warning must fire only for the
// both-off case.
func TestAnonymousEndpointsUnprotected(t *testing.T) {
	cases := []struct {
		abuseDisabled   bool
		rateLimiterWire bool
		want            bool
	}{
		{abuseDisabled: true, rateLimiterWire: false, want: true},
		{abuseDisabled: true, rateLimiterWire: true, want: false},
		{abuseDisabled: false, rateLimiterWire: false, want: false},
		{abuseDisabled: false, rateLimiterWire: true, want: false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, anonymousEndpointsUnprotected(c.abuseDisabled, c.rateLimiterWire),
			"abuseDisabled=%v rateLimiterWired=%v", c.abuseDisabled, c.rateLimiterWire)
	}
}
