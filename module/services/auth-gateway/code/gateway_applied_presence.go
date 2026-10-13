package main

import (
	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
)

// Read APPLIED presence only. Desired documents do not authorize traffic. The
// registration, applied generation and parsed document must name the same
// binding and generation.

// appliedPresence is the presence document this host APPLIED for the binding,
// and only when the registration, the applied generation and the parsed
// document name the same binding and generation.
func appliedPresence(binding *accountsv1.SolutionHostBindingState) (*solutionhost.SolutionHostBinding, bool) {
	applied := binding.GetApplied()
	record := binding.GetRegistration()
	declared := record.GetDeclared()
	if applied == nil || applied.GetRemoved() || declared == nil ||
		declared.GetBindingId() != binding.GetBindingId() ||
		declared.GetGeneration() != applied.GetGeneration().GetGeneration() ||
		applied.GetSolutionId() != record.GetSolutionId() || record.GetTombstonedAt() != nil {
		return nil, false
	}
	document, err := solutionhost.Parse([]byte(applied.GetGeneration().GetDocument()))
	if err != nil || document.Binding != declared.GetBindingId() || document.Generation != declared.GetGeneration() {
		return nil, false
	}
	return document, true
}

// routeGatewayAPIs are the APIs a declared route is served on: the REST surface
// a solution's passthrough transcodes to, or plain HTTP. A gRPC or Connect
// endpoint's address is not an HTTP origin the proxy can forward to.
var routeGatewayAPIs = map[string]bool{"rest": true, "http": true}

// moduleEndpoint names the endpoint a declared presence serves this gateway on:
// module, service, endpoint and API, exactly as Codefly keys an endpoint. It is
// a NAME, never an address — the presence declares endpoints and freezes no
// address — and the Codefly SDK resolves it to where it lives. The module is
// the Codefly module the service is composed under, which a solution is too.
type moduleEndpoint struct {
	Module, Service, Name, API string
}

// appliedRouteEndpoints are the endpoints an applied presence — a solution's
// or a module's, one rule for both — declares for the service that runs its
// workload. Which one this gateway forwards the route to is not decided here:
// it is the one the composition handed this gateway an address for (see
// solutionRegistryCache.resolveRouteUpstreams), because the composition is
// what knows which endpoint traffic reaches the workload through.
//
// Only the presence's own endpoints: a presence naming another module's
// endpoint could otherwise point its alias at any address this gateway holds a
// carrier for. "Own" is the Codefly module the alias names — the CLI composes a
// solution under a module whose name is its route alias, exactly as it does a
// module — so the rule is the same test for both kinds.
func appliedRouteEndpoints(binding *accountsv1.SolutionHostBindingState) []moduleEndpoint {
	document, ok := appliedPresence(binding)
	if !ok {
		return nil
	}
	serving := map[string]bool{}
	for _, workload := range document.Workloads {
		serving[workload.Artifact] = true
	}
	alias := binding.GetRegistration().GetSolutionId()
	var endpoints []moduleEndpoint
	for _, endpoint := range document.Endpoints {
		if serving[endpoint.Service] && endpoint.Module == alias && routeGatewayAPIs[endpoint.API] {
			endpoints = append(endpoints, moduleEndpoint{Module: endpoint.Module, Service: endpoint.Service, Name: endpoint.Name, API: endpoint.API})
		}
	}
	return endpoints
}
