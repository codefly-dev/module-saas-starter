package main

import (
	"errors"
	"fmt"

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
	manifest, refusal := readAppliedManifest(binding)
	return manifest, manifest != nil && refusal == nil
}

// The classes of refusal. The class is what the operator snapshot projects; the
// full reason, which can quote what the manifest carries (an endpoint address
// among it), is logged and never projected.
var (
	errManifestUnparsable = errors.New("the applied manifest is rejected by this gateway's parser")
	errManifestMismatch   = errors.New("the applied manifest names another binding, generation or alias than its registration")
)

// refusalClass is the projectable class of a readAppliedManifest refusal.
func refusalClass(refusal error) string {
	for _, class := range []error{errManifestUnparsable, errManifestMismatch} {
		if errors.Is(refusal, class) {
			return class.Error()
		}
	}
	return "the applied manifest is refused"
}

// readAppliedManifest tells the two reasons for no manifest apart. Nil and nil:
// nothing applied matches the registration yet, which reconciliation settles on
// its own. A refusal: a matching generation WAS applied and this gateway cannot
// use its document — the parser rejects it, or it names another binding,
// generation or alias. A refusal does not settle itself, so the caller reports it.
func readAppliedManifest(binding *accountsv1.SolutionHostBindingState) (*solutionhost.ModuleManifest, error) {
	applied := binding.GetApplied()
	record := binding.GetRegistration()
	declared := record.GetDeclared()
	if applied == nil || applied.GetRemoved() || declared == nil ||
		declared.GetBindingId() != binding.GetBindingId() ||
		declared.GetGeneration() != applied.GetGeneration().GetGeneration() ||
		applied.GetSolutionId() != record.GetSolutionId() || record.GetTombstonedAt() != nil {
		return nil, nil
	}
	manifest, err := solutionhost.Parse([]byte(applied.GetGeneration().GetDocument()))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errManifestUnparsable, err)
	}
	if manifest.Binding != declared.GetBindingId() || manifest.Generation != declared.GetGeneration() ||
		manifest.Alias != record.GetSolutionId() {
		return nil, fmt.Errorf("%w: it names binding %q generation %d alias %q", errManifestMismatch, manifest.Binding, manifest.Generation, manifest.Alias)
	}
	return manifest, nil
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
