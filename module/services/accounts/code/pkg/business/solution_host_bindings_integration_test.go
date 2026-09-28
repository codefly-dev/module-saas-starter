//go:build !pure

package business_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/google/uuid"

	"accounts/pkg/business"
)

// The reconciler against Postgres. The row lock, the partial unique index on the
// registry key and the whole-or-absent CHECK constraints only exist there, and
// they are what make two replicas converge and a restart resume. The admission
// rules themselves are decided without a database in
// solution_host_binding_admission_test.go.

// deliveredSet is a mount that hands over exactly what a test put in it.
type deliveredSet struct {
	documents []business.SolutionHostBindingDocument
	err       error
}

func (d *deliveredSet) Documents(context.Context) ([]business.SolutionHostBindingDocument, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.documents, nil
}

func (d *deliveredSet) put(t *testing.T, document *solutionhost.SolutionHostBinding) {
	t.Helper()
	data, err := solutionhost.Marshal(document)
	if err != nil {
		t.Fatalf("marshal %s: %v", document.Binding, err)
	}
	d.documents = []business.SolutionHostBindingDocument{{
		Source: "mount/" + document.Binding + ".codefly.yaml",
		Data:   data,
	}}
}

const testHostCoordinate = "acme/test/eu-west-1"

// declaredBinding builds a present generation for one solution on this host. The
// binding ID is dotted and the alias is a single lowercase segment, which is the
// shape the renderer commits to (codefly-dev/cli#853): the binding ID is never
// the registry key.
func declaredBinding(t *testing.T, solutionID string, generation uint64) *solutionhost.SolutionHostBinding {
	t.Helper()
	document := &solutionhost.SolutionHostBinding{
		Schema:     solutionhost.SchemaV1,
		Binding:    "acme.test." + solutionID,
		Generation: generation,
		Host:       solutionhost.HostTarget{Coordinate: testHostCoordinate, Component: "saas-host"},
		Release:    solutionhost.Release{Publisher: "acme", Name: solutionID, Version: "1.4.0"},
		Routes:     []solutionhost.Route{{Alias: solutionID, Surface: solutionhost.SurfaceBackend}},
		Artifacts: []solutionhost.Artifact{{
			Surface: solutionhost.SurfaceBackend,
			Name:    "api",
			Release: "acme/" + solutionID + "@1.4.0",
			Digest:  "sha256:" + strings.Repeat("ab", 32),
		}},
		Workload: solutionhost.WorkloadIdentity{
			Audience: "https://test.acme.example/solutions",
			Subject:  "system:serviceaccount:" + solutionID + ":api",
		},
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("test document is not a valid binding: %v", err)
	}
	return document
}

func tombstoneOf(t *testing.T, document *solutionhost.SolutionHostBinding, generation uint64) *solutionhost.SolutionHostBinding {
	t.Helper()
	removed := *document
	removed.Generation = generation
	removed.Routes = nil
	removed.Artifacts = nil
	removed.Modules = nil
	removed.Endpoints = nil
	removed.Removed = true
	if err := removed.Validate(); err != nil {
		t.Fatalf("test tombstone is not valid: %v", err)
	}
	return &removed
}

func testDeclaredSolutionID(t *testing.T) string {
	t.Helper()
	return "dsol" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

func newTestReconciler(t *testing.T, source business.SolutionHostBindingSource) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(
		testService, source, testHostCoordinate, time.Minute)
	if err != nil {
		t.Fatalf("reconciler: %v", err)
	}
	return reconciler
}

func bindingState(t *testing.T, bindingID string) *business.SolutionHostBindingRecord {
	t.Helper()
	records, err := testService.ListSolutionHostBindings(testCtx)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	for _, record := range records {
		if record.BindingID == bindingID {
			return record
		}
	}
	return nil
}

func registration(t *testing.T, solutionID string) *business.SolutionRegistration {
	t.Helper()
	records, _, err := testService.ListSolutionRegistrations(testCtx, true)
	if err != nil {
		t.Fatalf("list registrations: %v", err)
	}
	for _, record := range records {
		if record.SolutionID == solutionID {
			return record
		}
	}
	return nil
}

// A delivered generation becomes a durable registration record, and the record
// says which binding declared it. Nothing has heartbeated, so it is PENDING: that
// is the whole point — declared and not observed is a state an operator can see,
// where before it was indistinguishable from never deployed.
func TestSolutionHostBinding_DeliveredGenerationDeclaresAPendingRegistration(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 1)
	mount := &deliveredSet{}
	mount.put(t, document)

	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	record := bindingState(t, document.Binding)
	if record == nil || record.Applied == nil {
		t.Fatalf("binding was not applied: %+v", record)
	}
	if record.Applied.Generation != 1 || record.Applied.SolutionID != solutionID {
		t.Fatalf("applied = %+v, want generation 1 into %q", record.Applied, solutionID)
	}
	if record.PendingReason != "" {
		t.Fatalf("pending reason = %q, want none", record.PendingReason)
	}
	if record.Applied.Release != "acme/"+solutionID+"@1.4.0" {
		t.Fatalf("applied release = %q", record.Applied.Release)
	}

	declared := registration(t, solutionID)
	if declared == nil || declared.Declared == nil {
		t.Fatalf("registration was not declared: %+v", declared)
	}
	if declared.Declared.BindingID != document.Binding || declared.Declared.Generation != 1 {
		t.Fatalf("declaration = %+v", declared.Declared)
	}
	if got := declared.Status(time.Now().UTC()); got != business.SolutionRegistrationPending {
		t.Fatalf("status = %q, want pending: declared presence is not an observation", got)
	}
}

// A second pass over the same mount applies nothing. core answers DecisionCurrent
// because a host re-reads its mount every pass, and a registry revision that moved
// would churn every replica's cache once per interval, forever.
func TestSolutionHostBinding_RereadingTheSameGenerationChangesNothing(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 1))
	reconciler := newTestReconciler(t, mount)

	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := registration(t, solutionID)

	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	second := registration(t, solutionID)

	if second.Revision != first.Revision {
		t.Fatalf("revision moved on a re-read: %d then %d", first.Revision, second.Revision)
	}
}

// Two replicas read the same mount and both propose the same generation. The
// second to take the row lock finds it applied and writes nothing, so the
// registry revision does not move twice for one generation.
func TestSolutionHostBinding_TwoReplicasConvergeWithoutDoubleApplying(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 1)
	first, second := &deliveredSet{}, &deliveredSet{}
	first.put(t, document)
	second.put(t, document)

	if err := newTestReconciler(t, first).RunOnce(testCtx); err != nil {
		t.Fatalf("replica one: %v", err)
	}
	applied := registration(t, solutionID)

	if err := newTestReconciler(t, second).RunOnce(testCtx); err != nil {
		t.Fatalf("replica two: %v", err)
	}
	after := registration(t, solutionID)

	if after.Revision != applied.Revision {
		t.Fatalf("the second replica re-applied: revision %d then %d", applied.Revision, after.Revision)
	}
	record := bindingState(t, document.Binding)
	if record.Applied.Generation != 1 {
		t.Fatalf("applied generation = %d, want 1", record.Applied.Generation)
	}
}

// A restart resumes from the recorded applied generation instead of deriving it
// again. A fresh reconciler over the same mount is exactly that: nothing in it
// remembers the previous pass.
func TestSolutionHostBinding_RestartResumesFromTheRecordedGeneration(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 3)
	mount := &deliveredSet{}
	mount.put(t, document)

	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("before restart: %v", err)
	}
	applied := registration(t, solutionID)

	// A brand-new reconciler, as a restarted process has.
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if after := registration(t, solutionID); after.Revision != applied.Revision {
		t.Fatalf("a restart re-applied the generation: revision %d then %d", applied.Revision, after.Revision)
	}
}

// A generation older than the applied one is refused, and — this is the part that
// matters — the applied generation keeps serving. The refusal and its reason are
// recorded as pending state, so an operator sees that delivery is showing
// something stale rather than nothing happening.
func TestSolutionHostBinding_StaleGenerationLeavesTheRunningOneUntouched(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 4))
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("apply generation 4: %v", err)
	}
	applied := registration(t, solutionID)

	stale := declaredBinding(t, solutionID, 3)
	mount.put(t, stale)
	// The pass reports the refusal to its caller; the loop logs it and carries on.
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("a withheld document must not fail the pass: %v", err)
	}

	record := bindingState(t, stale.Binding)
	if record.Applied == nil || record.Applied.Generation != 4 {
		t.Fatalf("applied = %+v, want generation 4 still running", record.Applied)
	}
	if record.Desired == nil || record.Desired.Generation != 3 {
		t.Fatalf("desired = %+v, want the generation delivery is showing", record.Desired)
	}
	if record.PendingGeneration() != 3 {
		t.Fatalf("pending generation = %d, want 3", record.PendingGeneration())
	}
	if !strings.Contains(record.PendingReason, solutionhost.ErrStaleGeneration.Error()) {
		t.Fatalf("pending reason = %q, want it to name the stale generation", record.PendingReason)
	}
	if record.PendingSince == nil {
		t.Fatal("a pending reason must carry the instant it appeared")
	}
	if after := registration(t, solutionID); after.Revision != applied.Revision {
		t.Fatalf("a refused document moved the registry: revision %d then %d", applied.Revision, after.Revision)
	}
}

// A newer generation clears the pending reason as it applies, in the same write.
func TestSolutionHostBinding_ANewGenerationClearsThePendingReason(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 4))
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply generation 4: %v", err)
	}
	mount.put(t, declaredBinding(t, solutionID, 3))
	_ = reconciler.RunOnce(testCtx)
	if bindingState(t, "acme.test."+solutionID).PendingReason == "" {
		t.Fatal("the stale document should have left a pending reason")
	}

	mount.put(t, declaredBinding(t, solutionID, 5))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply generation 5: %v", err)
	}
	record := bindingState(t, "acme.test."+solutionID)
	if record.PendingReason != "" || record.PendingSince != nil {
		t.Fatalf("pending state survived the apply: %q since %v", record.PendingReason, record.PendingSince)
	}
	if record.Applied.Generation != 5 {
		t.Fatalf("applied generation = %d, want 5", record.Applied.Generation)
	}
}

// Removal is a tombstone generation: it withdraws the route and the record
// survives as a tombstone, so the removal is attributable and a late write from an
// older generation has something to collide with.
func TestSolutionHostBinding_RemovalIsATombstoneGeneration(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 4)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply the present generation: %v", err)
	}

	mount.put(t, tombstoneOf(t, present, 5))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply the tombstone: %v", err)
	}

	record := bindingState(t, present.Binding)
	if record.Applied == nil || !record.Applied.Removed || record.Applied.Generation != 5 {
		t.Fatalf("applied = %+v, want a tombstone at generation 5", record.Applied)
	}
	if len(record.Applied.Routes) != 0 {
		t.Fatalf("a tombstone holds no route, got %v", record.Applied.Routes)
	}
	if record.Applied.SolutionID != solutionID {
		t.Fatalf("a tombstone keeps the key it withdrew, got %q", record.Applied.SolutionID)
	}

	withdrawn := registration(t, solutionID)
	if withdrawn == nil || withdrawn.TombstonedAt == nil {
		t.Fatalf("the registration was not withdrawn: %+v", withdrawn)
	}
	if withdrawn.Frontend != nil || withdrawn.Backend != nil {
		t.Fatal("a withdrawal clears the endpoints in the write that records it")
	}
	if withdrawn.Declared == nil || withdrawn.Declared.Generation != 5 {
		t.Fatalf("the tombstone must keep its declaration: %+v", withdrawn.Declared)
	}
}

// After a declared removal, a heartbeat from the retiring runtime is refused —
// including one naming the tombstone's own revision, which is the path an
// UNDECLARED tombstone deliberately allows.
func TestSolutionHostBinding_AHeartbeatCannotEraseADeclaredTombstone(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 4)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	mount.put(t, tombstoneOf(t, present, 5))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	tombstone := registration(t, solutionID)

	revive := business.SolutionRegistrationWrite{
		SolutionID:       solutionID,
		Publisher:        tombstone.Publisher,
		ExpectedRevision: &tombstone.Revision,
		Lease:            time.Minute,
		Backend: &business.SolutionBackendRegistration{
			Upstream:     "http://retiring:8080",
			ServiceAlias: solutionID,
		},
	}
	_, err := testService.PutSolutionRegistration(testCtx, revive)
	if !errors.Is(err, business.ErrSolutionRegistrationDeclaredWithdrawn) {
		t.Fatalf("err = %v, want ErrSolutionRegistrationDeclaredWithdrawn", err)
	}
}

// The migration path: a solution that self-registered before it was declared keeps
// serving through the instant its presence becomes declared. Its halves and leases
// are observations, and discarding them would take a working solution offline at
// exactly the moment the operator declared it.
func TestSolutionHostBinding_DeclaringAnExistingRegistrationAdoptsItsObservations(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	publisher := "solution:" + solutionID

	if _, err := testService.PutSolutionRegistration(testCtx, business.SolutionRegistrationWrite{
		SolutionID: solutionID, Publisher: publisher, Lease: time.Minute,
		Frontend: &business.SolutionFrontendRegistration{Manifest: `{"id":"` + solutionID + `"}`},
	}); err != nil {
		t.Fatalf("self-registered frontend half: %v", err)
	}
	if _, err := testService.PutSolutionRegistration(testCtx, business.SolutionRegistrationWrite{
		SolutionID: solutionID, Publisher: publisher, Lease: time.Minute,
		Backend: &business.SolutionBackendRegistration{
			Upstream: "http://self-registered:8080", ServiceAlias: solutionID,
		},
	}); err != nil {
		t.Fatalf("self-registered backend half: %v", err)
	}
	before := registration(t, solutionID)
	if got := before.Status(time.Now().UTC()); got != business.SolutionRegistrationActive {
		t.Fatalf("status before declaring = %q, want active", got)
	}

	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 1))
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("declare it: %v", err)
	}

	after := registration(t, solutionID)
	if after.Declared == nil {
		t.Fatal("the record was not declared")
	}
	if got := after.Status(time.Now().UTC()); got != business.SolutionRegistrationActive {
		t.Fatalf("status after declaring = %q, want it still active", got)
	}
	if after.Backend == nil || after.Backend.Upstream != "http://self-registered:8080" {
		t.Fatalf("the observed upstream was discarded: %+v", after.Backend)
	}
	if after.Frontend == nil {
		t.Fatal("the observed manifest was discarded")
	}
	if after.Publisher != before.Publisher {
		t.Fatalf("publisher moved from %q to %q; a heartbeat would then fail its own publisher check",
			before.Publisher, after.Publisher)
	}

	// And the heartbeat keeps working against the now-declared record.
	if _, err := testService.PutSolutionRegistration(testCtx, business.SolutionRegistrationWrite{
		SolutionID: solutionID, Publisher: publisher, Lease: time.Minute,
		Backend: &business.SolutionBackendRegistration{
			Upstream: "http://self-registered:8080", ServiceAlias: solutionID,
		},
	}); err != nil {
		t.Fatalf("heartbeat after declaring: %v", err)
	}
}

// Deregistering a declared solution is refused: removal is a tombstone generation
// from delivery, and honouring the DELETE would remove the record for exactly as
// long as it takes the next pass to re-apply the declaration.
func TestSolutionHostBinding_DeregisteringADeclaredSolutionIsRefused(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 1))
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("declare: %v", err)
	}

	_, err := testService.DeleteSolutionRegistration(testCtx, solutionID, nil)
	if !errors.Is(err, business.ErrSolutionRegistrationDeclared) {
		t.Fatalf("err = %v, want ErrSolutionRegistrationDeclared", err)
	}
}

// An unreadable mount is not an empty desired set. Removal is a tombstone
// generation precisely so a volume that failed to mount can never be reconciled
// as "remove every solution".
func TestSolutionHostBinding_AnUnreadableMountRemovesNothing(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 1)
	mount := &deliveredSet{}
	mount.put(t, document)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("declare: %v", err)
	}
	applied := registration(t, solutionID)

	broken := &deliveredSet{err: errors.New("input/output error")}
	if err := newTestReconciler(t, broken).RunOnce(testCtx); err == nil {
		t.Fatal("an unreadable mount must be reported, not treated as an empty set")
	}

	record := bindingState(t, document.Binding)
	if record.Applied == nil || record.Applied.Removed {
		t.Fatalf("applied = %+v, want the generation untouched", record.Applied)
	}
	if after := registration(t, solutionID); after.TombstonedAt != nil || after.Revision != applied.Revision {
		t.Fatalf("an unreadable mount changed the registry: %+v", after)
	}
}

// A mount that goes EMPTY removes nothing either. A document disappearing is not
// a removal — that is what the tombstone generation is for — and a delivery tree
// that synced empty would otherwise withdraw every solution on the host.
func TestSolutionHostBinding_AnEmptyMountRemovesNothing(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 1)
	mount := &deliveredSet{}
	mount.put(t, document)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("declare: %v", err)
	}
	applied := registration(t, solutionID)

	mount.documents = nil
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("empty pass: %v", err)
	}

	record := bindingState(t, document.Binding)
	if record.Applied == nil || record.Applied.Removed {
		t.Fatalf("applied = %+v, want the generation still applied", record.Applied)
	}
	if after := registration(t, solutionID); after.TombstonedAt != nil || after.Revision != applied.Revision {
		t.Fatalf("an empty mount changed the registry: %+v", after)
	}
}

// A binding whose very first generation is refused still becomes visible, with
// its reason and no applied generation. That is the state the registry snapshot
// alone cannot show — there is no registration record at all — and it is why the
// binding listing exists.
func TestSolutionHostBinding_ABindingRefusedAtItsFirstGenerationIsStillVisible(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	unaddressable := declaredBinding(t, solutionID, 1)
	unaddressable.Routes[0].Alias = solutionID + ".internal"
	unaddressable.Artifacts[0].Surface = solutionhost.SurfaceBackend
	unaddressable.Routes[0].Surface = solutionhost.SurfaceBackend
	mount := &deliveredSet{}
	mount.put(t, unaddressable)

	if err := newTestReconciler(t, mount).RunOnce(testCtx); err == nil {
		t.Fatal("an unaddressable route must be reported")
	}

	record := bindingState(t, unaddressable.Binding)
	if record == nil {
		t.Fatal("a refused binding must still be recorded")
	}
	if record.Applied != nil {
		t.Fatalf("applied = %+v, want nothing applied", record.Applied)
	}
	if record.Desired == nil || record.Desired.Generation != 1 {
		t.Fatalf("desired = %+v, want generation 1", record.Desired)
	}
	if !strings.Contains(record.PendingReason, "addressable") {
		t.Fatalf("pending reason = %q, want it to name the route problem", record.PendingReason)
	}
	if registration(t, solutionID) != nil {
		t.Fatal("a refused generation must not create a registration record")
	}
}

// A refusal that stands across passes keeps the instant it started. Every pass
// re-reads the same mount and re-records the same refusal, so if the instant were
// replaced each time, a binding stuck for a day would read as one that started
// seconds ago — which is the opposite of what the field is for. This covers the
// apply-path refusal specifically, because that is the one a real deployment hits:
// an alias this host cannot address is refused when the generation is applied, not
// when it is admitted.
func TestSolutionHostBinding_AStandingRefusalKeepsWhenItStarted(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	unaddressable := declaredBinding(t, solutionID, 1)
	unaddressable.Routes[0].Alias = solutionID + ".internal"
	mount := &deliveredSet{}
	mount.put(t, unaddressable)
	reconciler := newTestReconciler(t, mount)

	if err := reconciler.RunOnce(testCtx); err == nil {
		t.Fatal("an unaddressable route must be reported")
	}
	first := bindingState(t, unaddressable.Binding)
	if first.PendingSince == nil {
		t.Fatal("a refusal must record when it started")
	}

	if err := reconciler.RunOnce(testCtx); err == nil {
		t.Fatal("the refusal must still stand on the next pass")
	}
	second := bindingState(t, unaddressable.Binding)
	if second.PendingSince == nil || !second.PendingSince.Equal(*first.PendingSince) {
		t.Fatalf("pending_since moved from %v to %v across passes; a standing refusal must keep when it started",
			first.PendingSince, second.PendingSince)
	}
	if second.PendingReason != first.PendingReason {
		t.Fatalf("the reason changed across passes: %q then %q", first.PendingReason, second.PendingReason)
	}

	// And a generation that does apply clears both.
	mount.put(t, declaredBinding(t, solutionID, 2))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("the addressable generation must apply: %v", err)
	}
	cleared := bindingState(t, unaddressable.Binding)
	if cleared.PendingReason != "" || cleared.PendingSince != nil {
		t.Fatalf("pending state survived the apply: %q since %v", cleared.PendingReason, cleared.PendingSince)
	}
}

// A route alias handed from one binding to another completes in one pass: the
// release is applied before the claim, so the claimant does not collide with the
// key the previous holder still records.
func TestSolutionHostBinding_AliasHandsOverInOnePass(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	first := declaredBinding(t, solutionID, 1)
	first.Binding = "acme.test." + solutionID + ".a"
	mount := &deliveredSet{}
	mount.put(t, first)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("first holder: %v", err)
	}

	second := declaredBinding(t, solutionID, 1)
	second.Binding = "acme.test." + solutionID + ".b"
	retiring := tombstoneOf(t, first, 2)
	firstData, err := solutionhost.Marshal(retiring)
	if err != nil {
		t.Fatalf("marshal tombstone: %v", err)
	}
	secondData, err := solutionhost.Marshal(second)
	if err != nil {
		t.Fatalf("marshal claimant: %v", err)
	}
	// Claimant first, which is the order a mount walk over these names produces.
	mount.documents = []business.SolutionHostBindingDocument{
		{Source: "mount/b.codefly.yaml", Data: secondData},
		{Source: "mount/a.codefly.yaml", Data: firstData},
	}

	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("hand-over pass: %v", err)
	}

	retired := bindingState(t, first.Binding)
	if retired.Applied == nil || !retired.Applied.Removed {
		t.Fatalf("the previous holder was not withdrawn: %+v", retired.Applied)
	}
	claimed := bindingState(t, second.Binding)
	if claimed == nil || claimed.Applied == nil {
		t.Fatalf("the claimant did not apply: %+v", claimed)
	}
	if claimed.Applied.SolutionID != solutionID {
		t.Fatalf("claimant applied into %q, want %q", claimed.Applied.SolutionID, solutionID)
	}
	live := registration(t, solutionID)
	if live.TombstonedAt != nil {
		t.Fatal("the hand-over left the record withdrawn")
	}
	if live.Declared == nil || live.Declared.BindingID != second.Binding {
		t.Fatalf("the record is declared by %+v, want the claimant", live.Declared)
	}
}
