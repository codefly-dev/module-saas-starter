package main

import (
	"context"
	"fmt"
	"sync"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/protobuf/types/known/timestamppb"

	"google.golang.org/protobuf/proto"
)

// fakeSolutionRegistry stands in for accounts in gateway tests. It applies
// halves and advances revisions so the cache, the reconcile loop, and the
// handlers can be exercised end to end, and it takes injected errors so the
// refusal paths (stale revision, tombstone, outage) can be driven without
// reimplementing the registry's rules here — those belong to the accounts
// tests that run against the real relation.
type fakeSolutionRegistry struct {
	mu       sync.Mutex
	records  map[string]*accountsv1.SolutionRegistration
	revision int64

	listErr error

	listCalls int
}

func newFakeSolutionRegistry() *fakeSolutionRegistry {
	return &fakeSolutionRegistry{records: map[string]*accountsv1.SolutionRegistration{}}
}

// fakeSolutionTarget is the target id the fake derives for an alias's FIRST
// declaration. It is a function of the alias so a test and the fake agree without
// passing ids around; declareTarget below is how a test says "a different
// binding now serves this alias", which no derivation can express.
func fakeSolutionTarget(alias string) string { return "target-" + alias }

// fakeFrontendBlock is the frontend block a declared solution's applied
// presence carries, as accounts copies it onto the record's frontend half: the
// author-declared half of the manifest the gateway projects.
func fakeFrontendBlock(alias string) string {
	return `{"title":"` + alias + ` page","order":7,"exposed_module":"./Page","host_contract":1}`
}

// fakeRouteEndpoint is the endpoint the fake's applied presence declares for
// an alias — the one the composition's carrier addresses — and route is the
// test binary's stand-in for that carrier: where the Codefly SDK resolves it.
// Nothing on the record names an address.
func fakeRouteEndpoint(alias string) moduleEndpoint {
	return moduleEndpoint{Module: alias, Service: alias, Name: "rest", API: "rest"}
}

func (f *fakeSolutionRegistry) route(alias, address string) {
	testModuleAddresses.Store(fakeRouteEndpoint(alias), address)
}

// seedDeclared creates a fully declared record, which is how a test gets one
// now that the runtime registration writer is deleted: delivery is what brings
// a record into being, and the composition is what addresses it.
func (f *fakeSolutionRegistry) seedDeclared(alias, upstream string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.route(alias, upstream)
	f.revision++
	f.records[alias] = &accountsv1.SolutionRegistration{
		SolutionId: alias,
		Publisher:  "solution:" + alias,
		Revision:   f.revision,
		Declared: &accountsv1.SolutionDeclaredBinding{
			BindingId:  "acme.test." + alias,
			Generation: 1,
			Release:    "acme/" + alias + "@1.0.0",
			TargetId:   fakeSolutionTarget(alias),
			// Every declaration states a kind: core requires it and refuses any
			// other value, so a fake record without one models a state delivery
			// cannot produce — and would quietly stop being routed at all.
			Kind: accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION,
		},
		Frontend: &accountsv1.SolutionFrontendBinding{Revision: f.revision, Manifest: fakeFrontendBlock(alias)},
	}
}

// declareTarget replaces the declaration on an existing record, so a test can
// model a REPLACEMENT presence: the same route alias, served by a different
// binding, under a target that is not the one any earlier installation named.
func (f *fakeSolutionRegistry) declareTarget(alias, bindingID, targetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.records[alias]
	if record == nil {
		return
	}
	f.revision++
	record.Revision = f.revision
	record.Declared = &accountsv1.SolutionDeclaredBinding{
		BindingId:  bindingID,
		Generation: 1,
		Release:    "acme/" + alias + "@2.0.0",
		TargetId:   targetID,
		Kind:       accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION,
	}
}

// tombstone applies a withdrawal to a record, which is how a declared presence
// stops being served: the row stays in the snapshot so diagnostics can see it,
// and routing treats it exactly like an alias nothing ever declared.
func (f *fakeSolutionRegistry) tombstone(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.records[alias]
	if record == nil {
		return
	}
	f.revision++
	record.Revision = f.revision
	record.TombstonedAt = timestamppb.Now()
}

// repointUpstream changes where the composition addresses a declared alias
// and advances the record, modelling a replacement delivery while a request is
// being admitted.
func (f *fakeSolutionRegistry) repointUpstream(alias, upstream string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.records[alias]
	if record == nil {
		return
	}
	f.route(alias, upstream)
	f.revision++
	record.Revision = f.revision
}

// undeclare strips a record's declaration, modelling presence nothing declared.
// Such a record resolves to no target and is admissible to nobody.
func (f *fakeSolutionRegistry) undeclare(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if record := f.records[alias]; record != nil {
		f.revision++
		record.Revision = f.revision
		record.Declared = nil
	}
}

func (f *fakeSolutionRegistry) List(
	_ context.Context, req *accountsv1.ListSolutionRegistrationsRequest,
) (*solutionRegistrySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &accountsv1.ListSolutionRegistrationsResponse{RegistryRevision: f.revision}
	for _, record := range f.records {
		if record.GetTombstonedAt() != nil && !req.GetIncludeTombstoned() {
			continue
		}
		listed := proto.Clone(record).(*accountsv1.SolutionRegistration)
		listed.Status = fakeSolutionStatus(listed)
		out.Registrations = append(out.Registrations, listed)
	}
	endpoints := make(map[string][]moduleEndpoint)
	for _, record := range out.Registrations {
		// What the applied presence declares, solution and module alike; where
		// it resolves is the carrier table's answer (route), never the record's.
		// Every record, declared or not: the tests about presence NOTHING
		// declared strip a record's declaration after the composition addressed
		// it, and need the address to stay so the verdict is what refuses it.
		endpoints[record.GetSolutionId()] = []moduleEndpoint{fakeRouteEndpoint(record.GetSolutionId())}
	}
	return &solutionRegistrySnapshot{ListSolutionRegistrationsResponse: out, routeEndpoints: endpoints}, nil
}

// testModuleAddresses is the test binary's stand-in for the composition's
// endpoint carriers: where the SDK would resolve each module endpoint.
var testModuleAddresses sync.Map

func init() {
	moduleEndpointResolver = func(_ context.Context, endpoint moduleEndpoint) (string, error) {
		address, ok := testModuleAddresses.Load(endpoint)
		if !ok || address.(string) == "" {
			return "", fmt.Errorf("no carrier for %s/%s/%s::%s", endpoint.Module, endpoint.Service, endpoint.Name, endpoint.API)
		}
		return address.(string), nil
	}
}

// fakeSolutionStatus mirrors business.(*SolutionRegistration).Status. A fake
// that derived it differently would hand tests a status the real registry
// never produces.
//
// A declared module is active once its presence is applied: it has no browser
// remote, so no frontend block is waited for. A solution is active once its
// applied presence carries one; until then it is pending.
func fakeSolutionStatus(record *accountsv1.SolutionRegistration) accountsv1.SolutionRegistrationStatus {
	switch {
	case record.GetTombstonedAt() != nil:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED
	case record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
	case record.GetFrontend() == nil:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING
	default:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
	}
}
