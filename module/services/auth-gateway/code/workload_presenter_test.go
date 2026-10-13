package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

const testWaypoint = "spiffe://cluster.local/ns/istio-presenter/sa/cell"
const testPresenter = "spiffe://cluster.local/ns/modules/sa/worker"

func TestWorkloadPresenterRequiresOneCanonicalWaypointAssertion(t *testing.T) {
	valid := "By=" + testWaypoint + ";URI=" + testPresenter
	for name, values := range map[string][]string{
		"missing": nil, "duplicate": {valid, valid}, "chain": {valid + "," + valid},
		"wrong waypoint": {"By=" + testPresenter + ";URI=" + testPresenter},
		"query":          {valid + "?"}, "fragment": {valid + "#x"},
		"extra field": {valid + ";Hash=abc"}, "quoted": {"By=" + testWaypoint + ";URI=\"" + testPresenter + "\""},
		"userinfo": {"By=" + testWaypoint + ";URI=spiffe://user@cluster.local/ns/modules/sa/worker"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := authenticatedWorkloadListener(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid presenter reached handler") }), testWaypoint)
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/modules/_work-context", nil)
			request.Header["X-Forwarded-Client-Cert"] = values
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
	for _, waypoint := range []string{"", "http://waypoint", testWaypoint + "?", testWaypoint + ";URI=x"} {
		_, err := authenticatedWorkloadListener(http.NotFoundHandler(), waypoint)
		require.Error(t, err)
	}
}

func TestWorkloadPresenterTravelsOnlyWithGatewayProvenance(t *testing.T) {
	gateway := &ExtAuthz{internalToken: "internal-test", gatewayToken: "gateway-test"}
	handler, err := authenticatedWorkloadListener(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, onWorkloadListener(r.Context()))
		require.Empty(t, r.Header.Get("X-Forwarded-Client-Cert"))
		require.Empty(t, r.Header.Get(workloadPresenterHeader))
		md, _ := metadata.FromOutgoingContext(gateway.moduleMintContext(r.Context()))
		require.Equal(t, []string{testPresenter}, md.Get(workloadPresenterHeader))
		require.Equal(t, []string{"gateway-test"}, md.Get("x-codefly-gateway-token"))
		require.Equal(t, []string{"internal-test"}, md.Get("x-codefly-internal-token"))
		w.WriteHeader(http.StatusNoContent)
	}), testWaypoint)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/modules/_work-context", nil)
	request.Header.Set("X-Forwarded-Client-Cert", "By="+testWaypoint+";URI="+testPresenter)
	request.Header.Set(workloadPresenterHeader, "forged")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code)

	// Native/public request metadata cannot supply the private listener identity.
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(workloadPresenterHeader, "forged", "x-codefly-gateway-token", "forged"))
	md, _ := metadata.FromOutgoingContext(gateway.moduleMintContext(ctx))
	require.Empty(t, md.Get(workloadPresenterHeader))
	require.Empty(t, md.Get("x-codefly-gateway-token"))
}
