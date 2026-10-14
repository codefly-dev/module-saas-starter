package main

import (
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/solutionhost"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// Exercise the production SDK resolver, not the test binary's address-map seam.
// The same gateway resolver sees addition, relocation and withdrawal without
// Init or an environment reload between deliveries.
func TestGatewayResolvesOnlyTheAppliedEndpointDelivery(t *testing.T) {
	t.Setenv(resources.ModulePrefix, "host")
	_, err := codefly.Init(t.Context())
	require.NoError(t, err)
	binding := modulePresenceBinding(t, endpointOf("documents", "rest", "rest"))
	endpoints := appliedRouteEndpoints(binding)
	require.Len(t, endpoints, 1)
	endpoint := endpoints[0]
	key := resources.EndpointAsEnvironmentVariableKey(&resources.EndpointInformation{
		Module: endpoint.Module, Service: endpoint.Service, Name: endpoint.Name, API: endpoint.API,
	})
	t.Setenv(key, "http://stale.example:8080")
	require.NoError(t, codefly.LoadEnvironmentVariables())
	deliver := func(document *solutionhost.SolutionHostBinding, address string) {
		t.Helper()
		raw, err := solutionhost.MarshalEndpointDelivery(document, map[string]string{key: address})
		require.NoError(t, err)
		binding.Applied.EndpointDelivery = raw
		projected := appliedRouteEndpoints(binding)
		require.Len(t, projected, 1)
		endpoint = projected[0]
	}
	_, err = sdkModuleEndpointAddress(t.Context(), endpoint)
	require.ErrorIs(t, err, codefly.ErrEndpointDeliveryUnavailable)
	deliver(endpoint.applied, "http://first.example:8081")
	address, err := sdkModuleEndpointAddress(t.Context(), endpoint)
	require.NoError(t, err)
	require.Contains(t, address, "first.example")
	next := *endpoint.applied
	next.Generation++
	deliver(&next, "http://second.example:8082")
	_, err = sdkModuleEndpointAddress(t.Context(), endpoint)
	require.ErrorIs(t, err, codefly.ErrEndpointDeliveryUnavailable)
	canonical, err := solutionhost.Marshal(&next)
	require.NoError(t, err)
	binding.Applied.Generation.Document = string(canonical)
	binding.Applied.Generation.Generation = next.Generation
	binding.Registration.Declared.Generation = next.Generation
	endpoints = appliedRouteEndpoints(binding)
	require.Len(t, endpoints, 1)
	endpoint = endpoints[0]
	address, err = sdkModuleEndpointAddress(t.Context(), endpoint)
	require.NoError(t, err)
	require.Contains(t, address, "second.example")
	binding.Applied.EndpointDelivery = nil
	endpoint = appliedRouteEndpoints(binding)[0]
	_, err = sdkModuleEndpointAddress(t.Context(), endpoint)
	require.ErrorIs(t, err, codefly.ErrEndpointDeliveryUnavailable)
}
