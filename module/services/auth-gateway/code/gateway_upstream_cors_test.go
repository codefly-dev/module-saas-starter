package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// accounts' generated CORS handler hands an empty allowlist to a library that
// reads empty as allow-all, so it answers a wildcard origin with credentials
// true. A grant on a response this gateway serves is this gateway's grant, so an
// upstream's own grant never reaches the public surface, and an empty allowlist
// grants nothing.
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

// A 1xx is informational: net/http sends it and keeps the same header map open for
// the real response the reverse proxy then fills from the upstream. Treating it as
// the response marked sanitization complete, so the final upstream headers — an
// upstream's own permissive grant among them — went out untouched.
//
// Driven through real local HTTP servers, because the behaviour is net/http's
// handling of an early hint and a recorder does not reproduce it.
func TestR1019CORSAfterInformationalResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// An early hint, exactly as a real upstream may send before its answer.
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-body"))
	}))
	t.Cleanup(upstream.Close)

	gw, _, _, _ := newGatewayHarness(t)
	upstreamURL, parseErr := url.Parse(upstream.URL)
	require.NoError(t, parseErr)
	gw.upstreams["accounts"] = upstreamURL

	front := httptest.NewServer(gw)
	t.Cleanup(front.Close)

	request, err := http.NewRequest(http.MethodGet, front.URL+"/v1/status", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://evil.example")

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	require.Equal(t, http.StatusOK, response.StatusCode)
	for name := range response.Header {
		require.NotContains(t, lowered(name), "access-control-",
			"the final response must carry no grant this gateway did not make: %s=%q",
			name, response.Header.Get(name))
	}
}
