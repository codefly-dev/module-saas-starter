package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// accounts' generated CORS handler hands an empty allowlist to a library that
// reads empty as allow-all, so it answers a wildcard origin with credentials
// true. Those headers used to be forwarded verbatim, which made the public
// surface grant a cross-origin read this gateway had granted nobody — and made an
// empty allowlist, which should grant nothing, grant everything.
//
// The generated handler is the go-grpc agent's to fix. What this gateway owns is
// that the grant on its own responses is the one it made.
func TestUpstreamCrossOriginGrantNeverReachesTheBrowser(t *testing.T) {
	gw, api, _, _ := newGatewayHarness(t)
	api.responseHeaders = http.Header{
		"Access-Control-Allow-Origin":      []string{"*"},
		"Access-Control-Allow-Credentials": []string{"true"},
		"Access-Control-Expose-Headers":    []string{"x-everything"},
	}

	for _, tc := range []struct {
		name   string
		origin string
	}{
		{"an unregistered origin", "https://evil.example"},
		{"no origin at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			recorder := httptest.NewRecorder()
			gw.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, "the request itself is unaffected")
			for name := range recorder.Header() {
				require.NotContains(t, lowered(name), "access-control-",
					"the gateway granted nothing, so no access-control header may leave it: %s=%q",
					name, recorder.Header().Get(name))
			}
		})
	}
}

// And a registered origin's grant is the gateway's own, replacing rather than
// joining the upstream's — two allow-origin values is a response every browser
// rejects.
func TestARegisteredOriginsGrantReplacesTheUpstreams(t *testing.T) {
	gw, api, _, _ := newGatewayHarness(t)
	twoRegisteredClients(t, gw)
	api.responseHeaders = http.Header{
		"Access-Control-Allow-Origin":      []string{"*"},
		"Access-Control-Allow-Credentials": []string{"true"},
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Origin", addinOrigin)
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)

	require.Equal(t, []string{addinOrigin}, recorder.Result().Header.Values("Access-Control-Allow-Origin"))
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"),
		"credentials are never granted cross-origin, whatever the upstream said")
}

func lowered(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}
