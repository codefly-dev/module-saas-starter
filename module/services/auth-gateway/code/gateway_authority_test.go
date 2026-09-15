package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayProxyUsesSelectedUpstreamAuthority(t *testing.T) {
	var gotHost, gotPath, gotQuery, gotBody string
	var gotHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath, gotQuery = r.Host, r.URL.Path, r.URL.RawQuery
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotBody, gotHeaders = string(body), r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	upstream, err := url.Parse(backend.URL + "/base?fixed=1")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "http://gateway.example.test/original?caller=2", strings.NewReader("payload"))
	request.Header.Set("Authorization", "Bearer opaque-test-token")
	request.Header.Set("X-Codefly-Gateway-Token", "untrusted")
	request.Header.Set("X-Codefly-Internal-Token", "untrusted")
	request.Header.Set("X-Codefly-Public-Origin", "https://untrusted.example.test")
	response := httptest.NewRecorder()
	gateway := &Gateway{}
	gateway.proxyTo(response, request, upstream, &RouteEntry{UpstreamPath: "/rewritten"})
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, upstream.Host, gotHost)
	require.Equal(t, "/base/rewritten", gotPath)
	require.Equal(t, "fixed=1&caller=2", gotQuery)
	require.Equal(t, "payload", gotBody)
	require.Equal(t, "Bearer opaque-test-token", gotHeaders.Get("Authorization"))
	for _, key := range []string{"X-Codefly-Gateway-Token", "X-Codefly-Internal-Token", "X-Codefly-Public-Origin"} {
		require.Empty(t, gotHeaders.Get(key))
	}
}
