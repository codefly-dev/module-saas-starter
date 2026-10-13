package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// A declared module is reached at the address the Codefly SDK resolves for the
// endpoint its applied presence declares — never at an address a registration
// records. These are the selection rule and the routing it feeds.

// modulePresenceBinding is an applied module presence whose one workload runs
// service "documents", declaring the given endpoints.
func modulePresenceBinding(t *testing.T, endpoints ...solutionhost.Endpoint) *accountsv1.SolutionHostBindingState {
	t.Helper()
	var document *solutionhost.SolutionHostBinding
	for _, fixture := range solutionhost.FixturesOf(solutionhost.DocumentTypePresence) {
		parsed, err := solutionhost.Parse(fixture.Document)
		if err == nil && len(parsed.Workloads) == 1 {
			document = parsed
			break
		}
	}
	require.NotNil(t, document, "Core must ship a readable one-workload presence fixture")
	copied := *document
	copied.Kind = solutionhost.KindModule
	copied.Workloads = append([]solutionhost.Workload{}, document.Workloads...)
	copied.Workloads[0].Artifact = "documents"
	artifacts := append([]solutionhost.Artifact{}, document.Artifacts...)
	for i := range artifacts {
		if artifacts[i].Name == document.Workloads[0].Artifact {
			artifacts[i].Name = "documents"
		}
	}
	copied.Artifacts = artifacts
	copied.Endpoints = endpoints
	require.NoError(t, copied.Validate())
	body, err := json.Marshal(&copied)
	require.NoError(t, err)
	return &accountsv1.SolutionHostBindingState{
		BindingId: copied.Binding,
		Applied: &accountsv1.SolutionHostBindingAppliedGeneration{SolutionId: "documents",
			Generation: &accountsv1.SolutionHostBindingGeneration{Generation: copied.Generation, Document: string(body)}},
		Registration: &accountsv1.SolutionRegistration{SolutionId: "documents",
			Declared: &accountsv1.SolutionDeclaredBinding{BindingId: copied.Binding, Generation: copied.Generation,
				Kind: accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE}},
	}
}

func endpointOf(service, name, api string) solutionhost.Endpoint {
	return solutionhost.Endpoint{Name: name, Service: service, Module: "documents", API: api, Visibility: "internal"}
}

func TestAModulePresenceOffersTheEndpointsOfTheServiceItRuns(t *testing.T) {
	binding := modulePresenceBinding(t,
		endpointOf("documents", "grpc", "grpc"), endpointOf("documents", "rest", "rest"), endpointOf("store", "rest", "rest"))
	require.Equal(t, []moduleEndpoint{
		{Module: "documents", Service: "documents", Name: "rest", API: "rest"},
	}, appliedRouteEndpoints(binding), "only the serving service's HTTP-family endpoints: not grpc, not another service's")

	foreign := modulePresenceBinding(t, solutionhost.Endpoint{Name: "rest", Service: "documents", Module: "accounts", API: "rest", Visibility: "internal"})
	require.Empty(t, appliedRouteEndpoints(foreign), "a presence may not name another module's endpoint as its own surface")

	var document solutionhost.SolutionHostBinding
	require.NoError(t, json.Unmarshal([]byte(binding.Applied.Generation.Document), &document))
	document.Kind = solutionhost.KindSolution
	body, err := json.Marshal(&document)
	require.NoError(t, err)
	binding.Applied.Generation.Document = string(body)
	require.Equal(t, []moduleEndpoint{{Module: "documents", Service: "documents", Name: "rest", API: "rest"}},
		appliedRouteEndpoints(binding), "a solution's presence offers its endpoints by the same rule")
}

// Of the endpoints a presence offers, the gateway forwards to the one the
// composition handed it an address for: exactly one, never chosen between.
func TestAModuleIsRoutedToTheEndpointTheCompositionAddressed(t *testing.T) {
	rest := moduleEndpoint{Module: "documents", Service: "documents", Name: "rest", API: "rest"}
	grpc := moduleEndpoint{Module: "documents", Service: "documents", Name: "grpc", API: "grpc"}
	for _, tc := range []struct {
		name      string
		addressed map[moduleEndpoint]string
		want      string
	}{
		{"the one addressed endpoint", map[moduleEndpoint]string{rest: "http://documents.documents.svc.cluster.local:8080"}, "http://documents.documents.svc.cluster.local:8080"},
		{"none addressed", map[moduleEndpoint]string{}, ""},
		{"two addressed", map[moduleEndpoint]string{rest: "http://localhost:8080", grpc: "http://localhost:9090"}, ""},
		{"an address the policy refuses", map[moduleEndpoint]string{rest: "http://8.8.8.8:80"}, ""},
		{"a refused address beside one that answers", map[moduleEndpoint]string{rest: "http://localhost:8080", grpc: "http://8.8.8.8:80"}, "http://localhost:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newSolutionRegistryCache(nil)
			cache.resolveEndpoint = func(_ context.Context, endpoint moduleEndpoint) (string, error) {
				if address, ok := tc.addressed[endpoint]; ok {
					return address, nil
				}
				return "", errors.New("carrier absent")
			}
			upstreams := cache.resolveRouteUpstreams(context.Background(), map[string][]moduleEndpoint{"documents": {grpc, rest}})
			if tc.want == "" {
				require.NotContains(t, upstreams, "documents")
				return
			}
			require.Equal(t, tc.want, upstreams["documents"].String())
		})
	}
}

// The address the SDK resolves is where traffic goes, asked for by the
// endpoint's declared name; the record carries no address at all.
func TestAModuleRouteGoesWhereTheSDKResolvesItsEndpoint(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	resolved := declareModule(t, gw, "example-module")
	endpoint := fakeRouteEndpoint("example-module")
	original, _ := testModuleAddresses.Load(endpoint)
	asked := 0
	previous := gw.solutions.resolveEndpoint
	gw.solutions.resolveEndpoint = func(_ context.Context, named moduleEndpoint) (string, error) {
		require.Equal(t, endpoint, named)
		asked++
		return original.(string), nil
	}
	t.Cleanup(func() { gw.solutions.resolveEndpoint = previous })
	require.NoError(t, gw.solutions.refresh(context.Background()))
	require.Equal(t, 1, asked)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
	request.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	gw.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "/v1/example-module/things", resolved.lastPath)
}

// No resolvable address, or one the upstream policy refuses, leaves the module
// unrouted — answered as not active, never guessed.
func TestAModuleWithNoUsableAddressIsNotRouted(t *testing.T) {
	for name, resolve := range map[string]func(context.Context, moduleEndpoint) (string, error){
		"no carrier":             func(context.Context, moduleEndpoint) (string, error) { return "", errors.New("carrier absent") },
		"a public address":       func(context.Context, moduleEndpoint) (string, error) { return "http://8.8.8.8:80", nil },
		"metadata address":       func(context.Context, moduleEndpoint) (string, error) { return "http://169.254.169.254:80", nil },
		"an address with a path": func(context.Context, moduleEndpoint) (string, error) { return "http://localhost:8080/v1", nil },
	} {
		t.Run(name, func(t *testing.T) {
			gw, _, _, priv := newGatewayHarness(t)
			module := declareModule(t, gw, "example-module")
			gw.solutions.resolveEndpoint = resolve
			require.NoError(t, gw.solutions.refresh(context.Background()))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/example-module/things", nil)
			request.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
			gw.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.Empty(t, module.lastPath)
		})
	}
}
