package main

import (
	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
)

// Read APPLIED presence only. Desired documents do not authorize traffic. The
// registration, applied generation and parsed document must name the same
// binding and generation. Presence can declare several workloads; until it
// identifies which serves an endpoint, only one distinct identity is routable.
func appliedWorkloadSPIFFEID(binding *accountsv1.SolutionHostBindingState) string {
	document, ok := appliedPresence(binding)
	if !ok {
		return ""
	}
	id := ""
	for _, workload := range document.Workloads {
		candidate := workload.Identity.SPIFFEID
		if !workloadSPIFFEPattern.MatchString(candidate) || (id != "" && id != candidate) {
			return ""
		}
		id = candidate
	}
	return id
}

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

// moduleGatewayAPIs are the APIs a module's /v1/<alias> surface may be served
// on: the REST surface a solution's passthrough transcodes to, or plain HTTP.
var moduleGatewayAPIs = map[string]bool{"rest": true, "http": true}

// moduleEndpoint names the endpoint a declared module serves this gateway on:
// module, service, endpoint and API, exactly as Codefly keys an endpoint. It is
// a NAME, never an address — the presence declares endpoints and freezes no
// address — and the Codefly SDK resolves it to where it lives.
type moduleEndpoint struct {
	Module, Service, Name, API string
}

// appliedModuleEndpoints are the endpoints an applied MODULE presence declares
// for the service that runs its workload. Which one this gateway forwards
// /v1/<alias> to is not decided here: it is the one the composition handed this
// gateway an address for (see solutionRegistryCache.resolveModuleUpstreams),
// because the composition is what knows which endpoint a consuming solution
// reaches the module through.
func appliedModuleEndpoints(binding *accountsv1.SolutionHostBindingState) []moduleEndpoint {
	document, ok := appliedPresence(binding)
	if !ok || document.Kind != solutionhost.KindModule {
		return nil
	}
	serving := map[string]bool{}
	for _, workload := range document.Workloads {
		serving[workload.Artifact] = true
	}
	// Only the HTTP-family APIs a /v1/<alias> surface is served on: a gRPC or
	// Connect endpoint's address is not an HTTP origin the proxy can forward to.
	// And only the module's own endpoints: a presence naming another module's
	// endpoint could otherwise point this alias at any address this gateway
	// holds a carrier for.
	alias := binding.GetRegistration().GetSolutionId()
	var endpoints []moduleEndpoint
	for _, endpoint := range document.Endpoints {
		if serving[endpoint.Service] && endpoint.Module == alias && moduleGatewayAPIs[endpoint.API] {
			endpoints = append(endpoints, moduleEndpoint{Module: endpoint.Module, Service: endpoint.Service, Name: endpoint.Name, API: endpoint.API})
		}
	}
	return endpoints
}
