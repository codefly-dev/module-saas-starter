package main

import (
	"context"
	"sync"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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

	listErr   error
	putErr    error
	deleteErr error

	listCalls int
	puts      []*accountsv1.PutSolutionRegistrationRequest
	deletes   []string
}

// errFakeSolutionNotFound mirrors the code accounts returns for a delete of a
// solution that never registered.
var errFakeSolutionNotFound = grpcstatus.Error(codes.NotFound, "solution registration not found")

func newFakeSolutionRegistry() *fakeSolutionRegistry {
	return &fakeSolutionRegistry{records: map[string]*accountsv1.SolutionRegistration{}}
}

func (f *fakeSolutionRegistry) Put(
	_ context.Context, req *accountsv1.PutSolutionRegistrationRequest,
) (*accountsv1.SolutionRegistration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, proto.Clone(req).(*accountsv1.PutSolutionRegistrationRequest))
	if f.putErr != nil {
		return nil, f.putErr
	}
	record := f.records[req.GetSolutionId()]
	// Mirror the registry's compare-and-swap and tombstone refusals. A fake that
	// accepts every write makes the gateway's own tombstone handling untestable:
	// it would resurrect a removed registration and the test asserting that
	// cannot happen would still pass.
	if record != nil {
		if req.ExpectedRevision != nil && req.GetExpectedRevision() != record.GetRevision() {
			return nil, grpcstatus.Error(codes.Aborted, "solution registration revision is stale")
		}
		if record.GetTombstonedAt() != nil && req.ExpectedRevision == nil {
			return nil, grpcstatus.Error(codes.FailedPrecondition, "solution registration is tombstoned")
		}
		if record.GetTombstonedAt() == nil && req.ExpectedRevision == nil &&
			fakeSolutionHalfDiffers(record, req) {
			return nil, grpcstatus.Error(codes.Aborted, "solution registration revision required")
		}
	}
	f.revision++
	if record == nil {
		record = &accountsv1.SolutionRegistration{
			SolutionId: req.GetSolutionId(),
			Publisher:  req.GetPublisher(),
			// Assigned once, by the registry, when the record is created — and
			// never again, through a replacement half, a tombstone or a
			// reactivation. A fake that re-drew it on any later write would make
			// the gateway's "the boundary is stable" tests prove nothing.
			RuntimeBoundary: "boundary-" + req.GetSolutionId(),
		}
		f.records[req.GetSolutionId()] = record
	}
	if record.GetTombstonedAt() != nil {
		// An admitted reactivation starts from a clean record, as accounts does.
		record.Frontend, record.Backend = nil, nil
	}
	record.TombstonedAt = nil
	record.Revision = f.revision
	lease := timestamppb.New(time.Now().Add(time.Duration(req.GetLeaseSeconds()) * time.Second))
	if half := req.GetFrontend(); half != nil {
		record.Frontend = &accountsv1.SolutionFrontendBinding{
			Revision:        f.revision,
			Manifest:        half.GetManifest(),
			ContractVersion: half.GetContractVersion(),
			LeaseExpiresAt:  lease,
		}
	}
	if half := req.GetBackend(); half != nil {
		record.Backend = &accountsv1.SolutionBackendBinding{
			Revision:        f.revision,
			Upstream:        half.GetUpstream(),
			ServiceAlias:    half.GetServiceAlias(),
			ContractVersion: half.GetContractVersion(),
			LeaseExpiresAt:  lease,
		}
	}
	record.Status = fakeSolutionStatus(record, time.Now())
	out := proto.Clone(record).(*accountsv1.SolutionRegistration)
	// As accounts does: the boundary leaves the registry on exactly one
	// response, the backend half's own write, because that half is the one that
	// mints and the one whose credential proved which solution it is.
	if req.GetBackend() == nil {
		out.RuntimeBoundary = ""
	}
	return out, nil
}

func (f *fakeSolutionRegistry) Delete(
	_ context.Context, req *accountsv1.DeleteSolutionRegistrationRequest,
) (*accountsv1.SolutionRegistration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, req.GetSolutionId())
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	record := f.records[req.GetSolutionId()]
	if record == nil {
		return nil, errFakeSolutionNotFound
	}
	f.revision++
	record.Revision = f.revision
	record.Frontend = nil
	record.Backend = nil
	record.TombstonedAt = timestamppb.Now()
	record.Status = accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_TOMBSTONED
	tombstone := proto.Clone(record).(*accountsv1.SolutionRegistration)
	tombstone.RuntimeBoundary = ""
	return tombstone, nil
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
		// No listing carries a boundary: a consumer holding the whole registry
		// would otherwise hold every solution's.
		listed.RuntimeBoundary = ""
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
	case front != nil && !front.GetLeaseExpiresAt().AsTime().After(now):
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_EXPIRED
	case backend != nil && !backend.GetLeaseExpiresAt().AsTime().After(now):
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_EXPIRED
	case front == nil || backend == nil:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_PENDING
	default:
		return accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
	}
}

// fakeSolutionHalfDiffers reports whether a write replaces a half the record
// already holds with different content — the case the registry refuses unless
// the caller names the revision it believes it is replacing.
func fakeSolutionHalfDiffers(
	record *accountsv1.SolutionRegistration, req *accountsv1.PutSolutionRegistrationRequest,
) bool {
	if half := req.GetFrontend(); half != nil {
		held := record.GetFrontend()
		return held != nil && (held.GetManifest() != half.GetManifest() ||
			held.GetContractVersion() != half.GetContractVersion())
	}
	if half := req.GetBackend(); half != nil {
		held := record.GetBackend()
		return held != nil && (held.GetUpstream() != half.GetUpstream() ||
			held.GetServiceAlias() != half.GetServiceAlias() ||
			held.GetContractVersion() != half.GetContractVersion())
	}
	return false
}
