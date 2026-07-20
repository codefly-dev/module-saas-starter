package main

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientIPIgnoresForwardingHeadersFromUntrustedPeer(t *testing.T) {
	trust := newProxyTrust("10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.10:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	req.Header.Set("X-Real-Ip", "198.51.100.98")
	require.Equal(t, "203.0.113.10", trust.clientIP(req))
}

func TestClientIPWalksTrustedProxyChainRightToLeft(t *testing.T) {
	trust := newProxyTrust("10.0.0.0/8,192.0.2.10")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.3:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.44, 192.0.2.10, 10.0.0.2")
	require.Equal(t, "198.51.100.44", trust.clientIP(req))
}

func TestClientIPRejectsPrependedSpoofBeforeUntrustedClient(t *testing.T) {
	trust := newProxyTrust("10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.3:443"
	req.Header.Set("X-Forwarded-For", "192.0.2.250, 198.51.100.44, 10.0.0.2")
	require.Equal(t, "198.51.100.44", trust.clientIP(req))
}

func TestClientIPFailsClosedOnMalformedOrOversizedChain(t *testing.T) {
	trust := newProxyTrust("10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.3:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.44, definitely-not-an-ip")
	require.Equal(t, "10.0.0.3", trust.clientIP(req))

	req.Header.Set("X-Forwarded-For", "1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1,1.1.1.1")
	require.Equal(t, "10.0.0.3", trust.clientIP(req))
}

func TestClientIPSupportsIPv6AndTrustedRealIP(t *testing.T) {
	trust := newProxyTrust("2001:db8:abcd::/48")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[2001:db8:abcd::10]:443"
	req.Header.Set("X-Forwarded-For", "2001:db8:ffff::42, 2001:db8:abcd::20")
	require.Equal(t, "2001:db8:ffff::42", trust.clientIP(req))

	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-Ip", "2001:db8:ffff::43")
	require.Equal(t, "2001:db8:ffff::43", trust.clientIP(req))
}
