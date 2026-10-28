package main

import (
	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
)

// Read APPLIED manifests only. Desired documents do not authorize traffic. The
// registration, applied generation and parsed manifest must name the same
// binding and generation.

// appliedManifest is the module manifest this host APPLIED for the binding,
// and only when the registration, the applied generation and the parsed
// manifest name the same binding and generation.
func appliedManifest(binding *accountsv1.SolutionHostBindingState) (*solutionhost.ModuleManifest, bool) {
	applied := binding.GetApplied()
	record := binding.GetRegistration()
	declared := record.GetDeclared()
	if applied == nil || applied.GetRemoved() || declared == nil ||
		declared.GetBindingId() != binding.GetBindingId() ||
		declared.GetGeneration() != applied.GetGeneration().GetGeneration() ||
		applied.GetSolutionId() != record.GetSolutionId() || record.GetTombstonedAt() != nil {
		return nil, false
	}
	manifest, err := solutionhost.Parse([]byte(applied.GetGeneration().GetDocument()))
	if err != nil || manifest.Binding != declared.GetBindingId() || manifest.Generation != declared.GetGeneration() ||
		manifest.Alias != record.GetSolutionId() {
		return nil, false
	}
	return manifest, true
}

// routeGatewayAPIs are the APIs a declared route is served on: the REST surface
// a solution's passthrough transcodes to, or plain HTTP. A gRPC or Connect
// endpoint's address is not an HTTP origin the proxy can forward to.
var routeGatewayAPIs = map[string]bool{"rest": true, "http": true}

// moduleEndpoint is one endpoint an applied manifest declares for this
// gateway to route to: module (the manifest's alias), service, endpoint and
// API, as Codefly keys an endpoint, and the address the signed manifest
// carries for it.
type moduleEndpoint struct {
	Module, Service, Name, API string
	// Address is where the manifest says the endpoint is reached: resolved
	// at render in a cell, at delivery natively. It is signed with the rest of
	// the manifest, so it is never a value this gateway or a caller chose.
	Address string
}

// appliedRouteEndpoints are the HTTP-family endpoints an applied manifest — a
// solution's or a module's, one rule for both — declares for the services that
// run as it. Which one this gateway forwards the route to is decided by
// solutionRegistryCache.resolveRouteUpstreams. A manifest's endpoints are its
// own by construction: each names one of its declared workloads.
func appliedRouteEndpoints(binding *accountsv1.SolutionHostBindingState) []moduleEndpoint {
	manifest, ok := appliedManifest(binding)
	if !ok {
		return nil
	}
	return manifestRouteEndpoints(manifest)
}

func manifestRouteEndpoints(manifest *solutionhost.ModuleManifest) []moduleEndpoint {
	serving := map[string]bool{}
	for _, workload := range manifest.Workloads {
		serving[workload.Service] = true
	}
	var endpoints []moduleEndpoint
	for _, endpoint := range manifest.Endpoints {
		if serving[endpoint.Service] && routeGatewayAPIs[endpoint.API] {
			endpoints = append(endpoints, moduleEndpoint{
				Module: manifest.Alias, Service: endpoint.Service, Name: endpoint.Name, API: endpoint.API,
				Address: endpoint.Address,
			})
		}
	}
	return endpoints
}
