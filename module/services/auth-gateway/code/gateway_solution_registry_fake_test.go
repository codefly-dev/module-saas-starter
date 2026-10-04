package main

import (
	"context"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

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
	}
}

// repointUpstream changes a declared record's observed endpoint to model a
// concurrent delivery observation while a request is being admitted.
func (f *fakeSolutionRegistry) repointUpstream(alias, upstream string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.records[alias]
	if record == nil || record.GetBackend() == nil {
		return
	}
	f.revision++
	record.Revision = f.revision
	record.Backend.Revision = f.revision
	record.Backend.Upstream = upstream
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
) (*accountsv1.ListSolutionRegistrationsResponse, error) {
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
		listed.Status = fakeSolutionStatus(listed, time.Now())
		out.Registrations = append(out.Registrations, listed)
	}
	return out, nil
}

// fakeSolutionStatus mirrors business.(*SolutionRegistration).Status, ordering
// included: expiry is evaluated over whichever halves are present and outranks
// pending, so a half that stopped renewing reads as dead rather than waiting.
// A fake that ranks these differently hands tests a status the real registry
// would never produce.
func fakeSolutionStatus(record *accountsv1.SolutionRegistration, now time.Time) accountsv1.SolutionRegistrationStatus {
	front, backend := record.GetFrontend(), record.GetBackend()
	switch {
	case record.GetTombstonedAt() != nil:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED
	case front != nil && backend != nil &&
		front.GetContractVersion() != "" && backend.GetContractVersion() != "" &&
		front.GetContractVersion() != backend.GetContractVersion():
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_INCOMPATIBLE
	case front == nil || backend == nil:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING
	default:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
	}
}
