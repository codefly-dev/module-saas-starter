//go:build !pure

package business_test

import (
	"context"
	"encoding/json"
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
	d.documents = []business.SolutionHostBindingDocument{{
		Source: "mount/" + document.Binding + ".codefly.yaml",
		Data:   testCarrier(t, document),
	}}
}

// The signer identity this test host attests delivery as. It is a keyless
// certificate SAN in the shape a workflow identity takes, because that is what a
// real BundleVerifier returns and what the host's signer policy is keyed by.
const testSignerIdentity = "https://signer.example/acme-release@refs/heads/main"

// testBundleVerifier stands in for sigstore-go. A real bundle needs a trust
// root, a transparency-log entry and a certificate that expires, so what is
// exercised here is the host's ORDERING — verify, then admit — rather than the
// cryptography, which has its own unit tests at the seam.
type testBundleVerifier struct{ signer string }

func (v testBundleVerifier) VerifyBundle(context.Context, []byte, json.RawMessage) (string, error) {
	return v.signer, nil
}

// testCarrier wraps a document in the signed carrier delivery ships: the
// canonical bytes verbatim, plus a bundle over them. Admission takes only
// verified values, so a mount that handed over bare documents would be refused —
// which is the behaviour TestSolutionHostBindingRefusesAnUnsignedDocument pins.
func testCarrier(t *testing.T, document *solutionhost.SolutionHostBinding) []byte {
	t.Helper()
	// CanonicalBytes, not Marshal: the signing input is the canonical JSON, and
	// PresenceFromVerified refuses a payload that is not the canonical encoding
	// of the document it decodes to.
	canonical, err := document.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical bytes for %s: %v", document.Binding, err)
	}
	carrier, err := solutionhost.MarshalSigned(&solutionhost.Signed{
		Schema:   solutionhost.SchemaSignedV1,
		Document: canonical,
		Bundle:   json.RawMessage(solutionhost.FixtureBundle),
	})
	if err != nil {
		t.Fatalf("marshal carrier for %s: %v", document.Binding, err)
	}
	return carrier
}

const testHostCoordinate = "acme/test/eu-west-1"

// The ownership domain this test host accepts delivery from. Core refuses a
// document from an unstated domain, which is what stops a writer claiming an
// unseen binding ID under a domain of its own choosing.
const testOwnershipDomain = "acme"

// declaredBinding builds a present generation for one solution on this host. The
// binding ID is dotted and the alias is a single lowercase segment, which is the
// shape the renderer commits to (codefly-dev/cli#853): the binding ID is never
// the registry key.
func declaredBinding(t *testing.T, solutionID string, generation uint64) *solutionhost.SolutionHostBinding {
	t.Helper()
	document := &solutionhost.SolutionHostBinding{
		Schema:           solutionhost.SchemaPresenceV2,
		Kind:             solutionhost.KindSolution,
		Binding:          "acme.test." + solutionID,
		Generation:       generation,
		OwnershipDomain:  testOwnershipDomain,
		EnvelopeRevision: 1,
		Host:             solutionhost.HostTarget{Coordinate: testHostCoordinate, Component: "saas-host"},
		// Three digests, three distinct values. Presence v2 requires the release
		// digest — a generation that omits it is one no authority document can be
		// matched to — and core types the three separately so that comparing a
		// rendered digest to an image digest is a compile error. Giving them
		// different bytes here is what keeps a test from passing on a mix-up the
		// types happen not to catch.
		Release: solutionhost.Release{
			Publisher: "acme", Name: solutionID, Version: "1.4.0",
			Digest: solutionhost.ReleaseDigest("sha256:" + strings.Repeat("ef", 32)),
		},
		Routes: []solutionhost.Route{{Alias: solutionID, Surface: solutionhost.SurfaceBackend}},
		Artifacts: []solutionhost.Artifact{{
			Surface: solutionhost.SurfaceBackend,
			Name:    "api",
			Release: "acme/" + solutionID + "@1.4.0",
			Digest:  solutionhost.RenderedDigest("sha256:" + strings.Repeat("ab", 32)),
		}},
		Workloads: []solutionhost.Workload{{
			Name:      "api",
			Artifact:  "api",
			Container: "api",
			Image: solutionhost.Image{
				Repository: "registry.example/acme/" + solutionID,
				Digest:     solutionhost.ImageDigest("sha256:" + strings.Repeat("cd", 32)),
			},
			Identity: solutionhost.WorkloadIdentity{
				Audience: "https://test.acme.example/solutions",
				Subject:  "system:serviceaccount:" + solutionID + ":api",
				SPIFFEID: "spiffe://acme.test/ns/" + solutionID + "/sa/api",
			},
			// A sidecar that must never be accepted as the authenticating
			// container. Core requires this list EXPLICITLY — empty declares
			// "there are none", absent is refused — because a plain slice
			// cannot tell those apart and a host that cannot tell has to choose
			// between refusing every workload and trusting any container that
			// asks. Naming one here rather than writing the empty list keeps
			// the non-degenerate case in the reconciler's own fixtures.
			NonAuthenticating: &[]string{"telemetry-sidecar"},
		}},
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
	removed.Workloads = nil
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
	reconciler, err := business.NewSolutionHostBindingReconciler(testService,
		business.SolutionHostBindingReconcilerConfig{
			Source:          source,
			Verifier:        testBundleVerifier{signer: testSignerIdentity},
			Coordinate:      testHostCoordinate,
			Domains:         []string{testOwnershipDomain},
			DomainsBySigner: map[string][]string{testSignerIdentity: {testOwnershipDomain}},
			Interval:        time.Minute,
		})
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
	if first == nil {
		t.Fatalf("no registration after the first pass: %+v", bindingState(t, "acme.test."+solutionID))
	}

	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	second := registration(t, solutionID)
	if second == nil {
		// The re-read refused. This is where an applied record core cannot
		// judge shows up: the pass withholds the whole set, so the record a
		// previous pass wrote is still there but nothing reconciles again.
		t.Fatalf("registration vanished on a re-read: %+v", bindingState(t, "acme.test."+solutionID))
	}

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
	firstData := testCarrier(t, retiring)
	secondData := testCarrier(t, second)
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

// The installable identity (the adversarial review's §9). These run against
// Postgres because the partial unique indexes and the row lock are the things
// under test: they are what make "mint a target if the binding has no live one"
// race-free, and what stops two replicas minting two identities for one presence.

func liveTarget(t *testing.T, bindingID string) *business.SolutionTarget {
	t.Helper()
	targets, err := testService.ListSolutionTargets(testCtx)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	for _, target := range targets {
		if target.BindingID == bindingID && target.Live() {
			return target
		}
	}
	return nil
}

func targetHistory(t *testing.T, bindingID string) []*business.SolutionTarget {
	t.Helper()
	targets, err := testService.ListSolutionTargets(testCtx)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	var history []*business.SolutionTarget
	for _, target := range targets {
		if target.BindingID == bindingID {
			history = append(history, target)
		}
	}
	return history
}

// A present generation mints the identity an organisation can install.
func TestSolutionTarget_APresentGenerationOpensAnInstallableIdentity(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	document := declaredBinding(t, solutionID, 1)
	mount := &deliveredSet{}
	mount.put(t, document)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("apply: %v", err)
	}

	target := liveTarget(t, document.Binding)
	if target == nil {
		t.Fatal("a present generation must open a target")
	}
	if target.SolutionID != solutionID || target.OpenedGeneration != 1 {
		t.Fatalf("target = %+v, want alias %q opened at generation 1", target, solutionID)
	}
	if target.ID == "" {
		t.Fatal("a target must carry an identity of its own")
	}
}

// Renaming the route keeps the identity. An installation named the identity, and
// a rename is not a new thing to install.
func TestSolutionTarget_ARenameKeepsTheIdentityAndMovesTheAlias(t *testing.T) {
	first := testDeclaredSolutionID(t)
	second := testDeclaredSolutionID(t)
	document := declaredBinding(t, first, 1)
	mount := &deliveredSet{}
	mount.put(t, document)
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply generation 1: %v", err)
	}
	before := liveTarget(t, document.Binding)

	renamed := declaredBinding(t, first, 2)
	renamed.Routes[0].Alias = second
	renamed.Artifacts[0].Release = renamed.Release.Identity()
	mount.put(t, renamed)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply the rename: %v", err)
	}

	after := liveTarget(t, document.Binding)
	if after == nil {
		t.Fatal("the target must survive a rename")
	}
	if after.ID != before.ID {
		t.Fatalf("identity moved from %q to %q; an installation named the old one", before.ID, after.ID)
	}
	if after.SolutionID != second {
		t.Fatalf("alias = %q, want it moved to %q", after.SolutionID, second)
	}
	if len(targetHistory(t, document.Binding)) != 1 {
		t.Fatalf("a rename minted a second identity: %d targets", len(targetHistory(t, document.Binding)))
	}
}

// A tombstone closes the identity, and the row survives as the evidence that
// consent ended.
func TestSolutionTarget_ATombstoneClosesTheIdentityAndKeepsTheRecord(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 4)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	opened := liveTarget(t, present.Binding)

	mount.put(t, tombstoneOf(t, present, 5))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	if liveTarget(t, present.Binding) != nil {
		t.Fatal("a tombstone must leave no live identity to install")
	}
	history := targetHistory(t, present.Binding)
	if len(history) != 1 {
		t.Fatalf("history = %d targets, want the closed one retained", len(history))
	}
	closed := history[0]
	if closed.ID != opened.ID {
		t.Fatalf("the closed target is not the one that was opened: %q vs %q", closed.ID, opened.ID)
	}
	if closed.ClosedGeneration == nil || *closed.ClosedGeneration != 5 {
		t.Fatalf("closed at %v, want the tombstone generation 5", closed.ClosedGeneration)
	}
}

// THE REVIEW'S §9 FAILURE, as a test: a second binding taking a withdrawn
// binding's route alias must get its OWN identity, so it cannot inherit an
// organisation's installation or its team exposure.
func TestSolutionTarget_AReusedAliasDoesNotInheritTheWithdrawnIdentity(t *testing.T) {
	alias := testDeclaredSolutionID(t)
	first := declaredBinding(t, alias, 1)
	first.Binding = "acme.test." + alias + ".x"
	mount := &deliveredSet{}
	mount.put(t, first)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("present the first binding: %v", err)
	}
	inherited := liveTarget(t, first.Binding)
	if inherited == nil {
		t.Fatal("the first binding must have a target to inherit")
	}

	// Withdraw it, which is what ends the consent an organisation gave.
	mount.put(t, tombstoneOf(t, first, 2))
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("withdraw the first binding: %v", err)
	}

	// A different binding now takes the alias the tombstone released.
	second := declaredBinding(t, alias, 1)
	second.Binding = "acme.test." + alias + ".y"
	mount.put(t, second)
	if err := newTestReconciler(t, mount).RunOnce(testCtx); err != nil {
		t.Fatalf("present the second binding on the reused alias: %v", err)
	}

	claimant := liveTarget(t, second.Binding)
	if claimant == nil {
		t.Fatal("the second binding must get a target of its own")
	}
	if claimant.ID == inherited.ID {
		t.Fatalf("the reused alias %q handed the withdrawn identity %q to binding %q; "+
			"an installation naming it would have transferred without anybody acting",
			alias, inherited.ID, second.Binding)
	}
	if claimant.SolutionID != alias {
		t.Fatalf("claimant alias = %q, want the reused %q", claimant.SolutionID, alias)
	}
	// And the withdrawn identity stays closed, so nothing resolves through it.
	for _, target := range targetHistory(t, first.Binding) {
		if target.Live() {
			t.Fatalf("the withdrawn binding %q still has a live identity", first.Binding)
		}
	}
}

// A tombstone is TERMINAL: re-presenting a withdrawn binding ID is refused, and
// a replacement instance needs a new ID.
//
// This test previously asserted the weaker rule — that re-presenting the same ID
// minted a new identity — and core cd443989 replaced it with a refusal, for the
// reason this PR's own installation-target decision rests on: the binding ID is
// the handle every other system holds (installations, operation bindings, team
// grants), so reusing it makes a replacement indistinguishable from continuity,
// which is the one thing a withdrawal exists to make distinguishable. Minting a
// new identity under the old ID still left every holder of that ID re-attached
// to a different instance.
func TestSolutionTarget_RePresentingAWithdrawnBindingIsRefused(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	present := declaredBinding(t, solutionID, 1)
	mount := &deliveredSet{}
	mount.put(t, present)
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	original := liveTarget(t, present.Binding)

	mount.put(t, tombstoneOf(t, present, 2))
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	// The same binding ID, at a higher generation. Delivery is entitled to ship
	// it; the host is not entitled to apply it.
	again := declaredBinding(t, solutionID, 3)
	mount.put(t, again)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("re-present pass: %v", err)
	}

	record := bindingState(t, present.Binding)
	if record == nil {
		t.Fatal("the withdrawn binding lost its record")
	}
	if !strings.Contains(record.PendingReason, solutionhost.ErrTombstoned.Error()) {
		t.Fatalf("pending reason = %q, want it to name the terminal tombstone", record.PendingReason)
	}
	if record.Applied == nil || !record.Applied.Removed {
		t.Fatalf("the withdrawal was undone by a re-presentation: %+v", record.Applied)
	}
	if revived := liveTarget(t, present.Binding); revived != nil {
		t.Fatalf("re-presenting a withdrawn binding opened target %q; a tombstone is terminal", revived.ID)
	}

	// And the replacement path: a genuinely new instance has a genuinely new
	// binding ID, which opens a target of its own. Without this half the test
	// would pin "withdrawal is final" without pinning "replacement is possible",
	// and the refusal would be indistinguishable from a dead end.
	replacement := declaredBinding(t, solutionID, 1)
	replacement.Binding = present.Binding + "r"
	mount.put(t, replacement)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("replacement pass: %v", err)
	}
	revived := liveTarget(t, replacement.Binding)
	if revived == nil {
		t.Fatal("a replacement under a new binding ID must open a target")
	}
	if revived.ID == original.ID {
		t.Fatalf("the replacement reused the withdrawn identity %q", original.ID)
	}
	// The withdrawn binding's own history stays closed at one period: the
	// refused re-presentation added nothing, and the replacement's period
	// belongs to the replacement's ID.
	if history := targetHistory(t, present.Binding); len(history) != 1 {
		t.Fatalf("withdrawn binding history = %d, want the one closed period", len(history))
	}
	if history := targetHistory(t, replacement.Binding); len(history) != 1 {
		t.Fatalf("replacement history = %d, want one open period", len(history))
	}
}

// Re-reading the same generation mints nothing: the pass is a poll, and a target
// per pass would be a new installable identity every thirty seconds.
func TestSolutionTarget_RereadingTheSameGenerationMintsNothing(t *testing.T) {
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}
	mount.put(t, declaredBinding(t, solutionID, 1))
	reconciler := newTestReconciler(t, mount)
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := reconciler.RunOnce(testCtx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := len(targetHistory(t, "acme.test."+solutionID)); got != 1 {
		t.Fatalf("history = %d targets after two passes, want 1", got)
	}
}

// testReconcilerAcceptingDomains is newTestReconciler with more than one
// accepted ownership domain, and the signer allowed to speak for all of them.
//
// It exists for exactly one test, and the reason is that the default reconciler
// would make that test pass for the wrong reason: with a single accepted domain,
// a document under a second domain is refused by the host's own Domains list
// before the applied record is ever consulted, so the assertion would hold while
// proving nothing about the rule it names.
func testReconcilerAcceptingDomains(
	t *testing.T, source business.SolutionHostBindingSource, domains ...string,
) *business.SolutionHostBindingReconciler {
	t.Helper()
	reconciler, err := business.NewSolutionHostBindingReconciler(testService,
		business.SolutionHostBindingReconcilerConfig{
			Source:          source,
			Verifier:        testBundleVerifier{signer: testSignerIdentity},
			Coordinate:      testHostCoordinate,
			Domains:         domains,
			DomainsBySigner: map[string][]string{testSignerIdentity: domains},
			Interval:        time.Minute,
		})
	if err != nil {
		t.Fatalf("reconciler: %v", err)
	}
	return reconciler
}

// An applied binding is not taken over by a delivery under a different ownership
// domain, even at a higher generation and from a signer the host lets speak for
// that domain.
//
// This is the takeover case, and the host is the last thing standing in it.
// cli#855 reports that the renderer cannot catch it: AdmitRendered carries no
// applied state, so a publish folds presence generations without the one rule
// that needs a record — core's `record.Domain != document.OwnershipDomain` — and
// a presence-only module (no contract, so no authority gate either) reaches the
// delivery Job with a domain change that only this host will refuse.
//
// Everything that could refuse it for a cheaper reason is deliberately removed:
// the generation is HIGHER, so it is not stale; both domains are accepted by the
// host, so Domains does not refuse it; and the signer may speak for both, so
// DomainsBySigner does not either. What is left is the applied record, which is
// the rule under test.
func TestSolutionHostBinding_ADomainChangeDoesNotTakeOverAnAppliedBinding(t *testing.T) {
	const otherDomain = "other"
	solutionID := testDeclaredSolutionID(t)
	mount := &deliveredSet{}

	mount.put(t, declaredBinding(t, solutionID, 4))
	if err := testReconcilerAcceptingDomains(t, mount, testOwnershipDomain, otherDomain).RunOnce(testCtx); err != nil {
		t.Fatalf("apply generation 4: %v", err)
	}
	applied := registration(t, solutionID)

	// The same binding ID, a newer generation, a different owner.
	takeover := declaredBinding(t, solutionID, 5)
	takeover.OwnershipDomain = otherDomain
	mount.put(t, takeover)
	if err := testReconcilerAcceptingDomains(t, mount, testOwnershipDomain, otherDomain).RunOnce(testCtx); err != nil {
		t.Fatalf("a withheld document must not fail the pass: %v", err)
	}

	record := bindingState(t, takeover.Binding)
	if record.Applied == nil || record.Applied.Generation != 4 {
		t.Fatalf("applied = %+v, want generation 4 still running", record.Applied)
	}
	if record.Applied.Domain != testOwnershipDomain {
		t.Fatalf("applied domain = %q, want %q: the takeover moved ownership",
			record.Applied.Domain, testOwnershipDomain)
	}
	if !strings.Contains(record.PendingReason, solutionhost.ErrWrongDomain.Error()) {
		t.Fatalf("pending reason = %q, want it to name the domain conflict", record.PendingReason)
	}
	// The APPLIED-RECORD rule specifically, by the only wording that is unique
	// to it. core answers ErrWrongDomain for two different rules — "is delivered
	// under domain X, which host Y does not accept" and "was applied under
	// domain X and this document declares Y" — and asserting the sentinel alone
	// cannot tell them apart. That is not hypothetical: an earlier version of
	// this test asserted the sentinel plus "names both domains", and it passed
	// with the second domain removed from the host's accepted list, because
	// testHostCoordinate is "acme/test/eu-west-1" and so the host-accepts
	// message contains "acme" and "other" as well. It proved nothing about the
	// rule it is named for.
	if !strings.Contains(record.PendingReason, "was applied under domain") {
		t.Fatalf("pending reason = %q, want the APPLIED-RECORD refusal; a host-accepts refusal carries the same sentinel and would pass a weaker assertion",
			record.PendingReason)
	}
	// And that it is actionable: a legitimate rename is indistinguishable from a
	// takeover here, so the row must name which domain holds the binding and
	// which one asked for it. This is past verification, so it carries core's
	// wording rather than the coarse pre-verification reason.
	if !strings.Contains(record.PendingReason, testOwnershipDomain) ||
		!strings.Contains(record.PendingReason, otherDomain) {
		t.Fatalf("pending reason = %q, want it to name both %q and %q",
			record.PendingReason, testOwnershipDomain, otherDomain)
	}
	if after := registration(t, solutionID); after.Revision != applied.Revision {
		t.Fatalf("a refused takeover moved the registry: revision %d then %d", applied.Revision, after.Revision)
	}
}
