package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// A declared module is reached at the address its applied module manifest
// carries for the endpoint it declares — never at an address a registration
// records. These are the selection rule and the routing it feeds.

// moduleManifestBinding is an applied module manifest whose one workload runs
// service "documents", declaring the given endpoints.
func moduleManifestBinding(t *testing.T, endpoints ...solutionhost.Endpoint) *accountsv1.SolutionHostBindingState {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test.documents", Alias: "documents",
		Kind: solutionhost.KindModule, Generation: 1,
		Host:      solutionhost.HostTarget{Coordinate: "example-host", Domain: "example"},
		Release:   solutionhost.Release{Publisher: "acme", Name: "documents", Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "documents", Container: "documents"}, {Service: "store", Container: "store"}},
		Endpoints: endpoints,
	}
	body, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)
	return &accountsv1.SolutionHostBindingState{
		BindingId: manifest.Binding,
		Applied: &accountsv1.SolutionHostBindingAppliedGeneration{SolutionId: "documents",
			Generation: &accountsv1.SolutionHostBindingGeneration{Generation: manifest.Generation, Document: string(body)}},
		Registration: &accountsv1.SolutionRegistration{SolutionId: "documents",
			Declared: &accountsv1.SolutionDeclaredBinding{BindingId: manifest.Binding, Generation: manifest.Generation,
				Kind: accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE}},
	}
}

func endpointOf(service, name, api string) solutionhost.Endpoint {
	return solutionhost.Endpoint{Name: name, Service: service, API: api, Address: "http://" + service + ":8080"}
}

func TestAModuleManifestOffersTheHTTPEndpointsOfItsWorkloads(t *testing.T) {
	binding := moduleManifestBinding(t, endpointOf("documents", "grpc", "grpc"), endpointOf("documents", "rest", "rest"))
	_, ok := appliedManifest(binding)
	require.True(t, ok)
	require.Equal(t, []moduleEndpoint{
		{Module: "documents", Service: "documents", Name: "rest", API: "rest", Address: "http://documents:8080"},
	}, appliedRouteEndpoints(binding), "only HTTP-family endpoints: not grpc")

	binding.Registration.SolutionId = "another"
	binding.Applied.SolutionId = "another"
	require.Empty(t, appliedRouteEndpoints(binding), "a manifest is routed only under its own alias")
}

// The production resolver answers the address the applied manifest carries,
// and refuses an endpoint that carries none.
func TestTheManifestAddressIsWhereAnEndpointLives(t *testing.T) {
	endpoint := appliedRouteEndpoints(moduleManifestBinding(t, endpointOf("documents", "rest", "rest")))[0]
	address, err := manifestEndpointAddress(context.Background(), endpoint)
	require.NoError(t, err)
	require.Equal(t, "http://documents:8080", address)
	endpoint.Address = ""
	_, err = manifestEndpointAddress(context.Background(), endpoint)
	require.Error(t, err)
}

// Of the endpoints a presence offers, the gateway forwards to the one the
// composition handed it an address for: exactly one, never chosen between.
func TestAModuleIsRoutedToTheEndpointTheCompositionAddressed(t *testing.T) {
	rest := moduleEndpoint{Module: "documents", Service: "documents", Name: "rest", API: "rest"}
	grpc := moduleEndpoint{Module: "documents", Service: "documents", Name: "grpc", API: "grpc"}
	for _, tc := range []struct {
		name      string
		addressed map[string]string
		want      string
	}{
		{"the one addressed endpoint", map[string]string{rest.String(): "http://documents.documents.svc.cluster.local:8080"}, "http://documents.documents.svc.cluster.local:8080"},
		{"none addressed", map[string]string{}, ""},
		{"two addressed", map[string]string{rest.String(): "http://localhost:8080", grpc.String(): "http://localhost:9090"}, ""},
		{"an address the policy refuses", map[string]string{rest.String(): "http://8.8.8.8:80"}, ""},
		{"a refused address beside one that answers", map[string]string{rest.String(): "http://localhost:8080", grpc.String(): "http://8.8.8.8:80"}, "http://localhost:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newSolutionRegistryCache(nil)
			cache.resolveEndpoint = func(_ context.Context, endpoint moduleEndpoint) (string, error) {
				if address, ok := tc.addressed[endpoint.String()]; ok {
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
	original, _ := testModuleAddresses.Load(endpoint.String())
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
