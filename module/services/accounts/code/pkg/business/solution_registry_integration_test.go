package business_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"accounts/pkg/business"
)

// These exercise the registry against Postgres: the revision sequence, the row
// lock, and the half-whole/tombstone CHECK constraints only exist there. The
// pure compare-and-swap rules are covered in solution_registry_test.go.

func testSolutionID(t *testing.T) string {
	t.Helper()
	return "sol" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
}

func frontendWrite(id, publisher, manifest string) business.SolutionRegistrationWrite {
	return business.SolutionRegistrationWrite{
		SolutionID: id,
		Publisher:  publisher,
		Lease:      time.Minute,
		Frontend:   &business.SolutionFrontendRegistration{Manifest: manifest},
	}
}

func backendWrite(id, publisher, upstream string) business.SolutionRegistrationWrite {
	return business.SolutionRegistrationWrite{
		SolutionID: id,
		Publisher:  publisher,
		Lease:      time.Minute,
		Backend: &business.SolutionBackendRegistration{
			Upstream:     upstream,
			ServiceAlias: id,
		},
	}
}

func TestSolutionRegistry_HalvesConvergeOnOneRecord(t *testing.T) {
	id := testSolutionID(t)

	record, err := testService.PutSolutionRegistration(testCtx, frontendWrite(id, "acme", `{"id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("frontend half: %v", err)
	}
	if got := record.Status(time.Now().UTC()); got != business.SolutionRegistrationPending {
		t.Fatalf("status after one half = %q, want pending", got)
	}
	frontendRevision := record.Revision

	record, err = testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080"))
	if err != nil {
		t.Fatalf("backend half: %v", err)
	}
	if got := record.Status(time.Now().UTC()); got != business.SolutionRegistrationActive {
		t.Fatalf("status after both halves = %q, want active", got)
	}
	if record.Revision <= frontendRevision {
		t.Fatalf("revision did not advance: %d then %d", frontendRevision, record.Revision)
	}
	if record.Frontend == nil || record.Backend == nil {
		t.Fatal("both halves must survive on one record")
	}
}

// Restart recovery and multi-replica convergence both reduce to this: the
// snapshot a consumer rebuilds from is authoritative and carries a revision.
func TestSolutionRegistry_SnapshotCarriesRegistryRevision(t *testing.T) {
	id := testSolutionID(t)
	if _, err := testService.PutSolutionRegistration(testCtx, frontendWrite(id, "acme", `{"id":"`+id+`"}`)); err != nil {
		t.Fatalf("frontend half: %v", err)
	}
	written, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080"))
	if err != nil {
		t.Fatalf("backend half: %v", err)
	}

	records, revision, err := testService.ListSolutionRegistrations(testCtx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if revision < written.Revision {
		t.Fatalf("registry revision %d is behind the record's %d", revision, written.Revision)
	}
	var found *business.SolutionRegistration
	for _, candidate := range records {
		if candidate.SolutionID == id {
			found = candidate
		}
	}
	if found == nil {
		t.Fatal("registration missing from the snapshot")
	}
	if found.Backend == nil || found.Backend.Upstream != "http://upstream.example:8080" {
		t.Fatalf("backend half did not round-trip: %+v", found.Backend)
	}
}

func TestSolutionRegistry_RenewalKeepsRevision(t *testing.T) {
	id := testSolutionID(t)
	first, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	renewed, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080"))
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if renewed.Revision != first.Revision {
		t.Fatalf("renewal advanced the revision %d -> %d", first.Revision, renewed.Revision)
	}
	if !renewed.Backend.LeaseExpiresAt.After(first.Backend.LeaseExpiresAt) {
		t.Fatal("renewal did not extend the lease")
	}
}

func TestSolutionRegistry_StalePublisherCannotOverwrite(t *testing.T) {
	id := testSolutionID(t)
	first, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	moved := backendWrite(id, "acme", "http://upstream-2.example:8080")
	moved.ExpectedRevision = &first.Revision
	second, err := testService.PutSolutionRegistration(testCtx, moved)
	if err != nil {
		t.Fatalf("compare-and-swap update: %v", err)
	}

	// The first publisher retries against the revision it still holds.
	behind := backendWrite(id, "acme", "http://upstream-3.example:8080")
	behind.ExpectedRevision = &first.Revision
	if _, err := testService.PutSolutionRegistration(testCtx, behind); err != business.ErrSolutionRegistrationStale {
		t.Fatalf("err = %v, want stale", err)
	}

	current, _, err := testService.ListSolutionRegistrations(testCtx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, candidate := range current {
		if candidate.SolutionID == id && candidate.Backend.Upstream != "http://upstream-2.example:8080" {
			t.Fatalf("stale write landed: %q", candidate.Backend.Upstream)
		}
		if candidate.SolutionID == id && candidate.Revision != second.Revision {
			t.Fatalf("revision = %d, want %d", candidate.Revision, second.Revision)
		}
	}
}

func TestSolutionRegistry_ForeignPublisherRefused(t *testing.T) {
	id := testSolutionID(t)
	if _, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080")); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "other", "http://attacker.example:8080")); err != business.ErrSolutionPublisherMismatch {
		t.Fatalf("err = %v, want publisher mismatch", err)
	}
}

// Deletion must remove routing and page availability in one write, and the
// tombstone it leaves must survive a delayed heartbeat from the old deployment.
func TestSolutionRegistry_DeleteTombstonesAndBlocksResurrection(t *testing.T) {
	id := testSolutionID(t)
	if _, err := testService.PutSolutionRegistration(testCtx, frontendWrite(id, "acme", `{"id":"`+id+`"}`)); err != nil {
		t.Fatalf("frontend half: %v", err)
	}
	if _, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080")); err != nil {
		t.Fatalf("backend half: %v", err)
	}

	tombstone, err := testService.DeleteSolutionRegistration(testCtx, id, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if tombstone.TombstonedAt == nil || tombstone.Frontend != nil || tombstone.Backend != nil {
		t.Fatalf("tombstone kept endpoints: %+v", tombstone)
	}

	records, _, err := testService.ListSolutionRegistrations(testCtx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, candidate := range records {
		if candidate.SolutionID == id {
			t.Fatal("a tombstoned registration is still being served")
		}
	}

	// The retiring deployment heartbeats on.
	if _, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080")); err != business.ErrSolutionRegistrationTombstoned {
		t.Fatalf("err = %v, want tombstoned", err)
	}

	// And a deliberate re-registration, naming the tombstone, is admitted.
	reactivate := backendWrite(id, "acme", "http://upstream.example:8080")
	reactivate.ExpectedRevision = &tombstone.Revision
	revived, err := testService.PutSolutionRegistration(testCtx, reactivate)
	if err != nil {
		t.Fatalf("reactivation: %v", err)
	}
	if revived.TombstonedAt != nil {
		t.Fatal("reactivation left the tombstone in place")
	}
	if revived.Frontend != nil {
		t.Fatal("reactivation must not restore the half the publisher did not re-register")
	}
}

func TestSolutionRegistry_DeleteIsIdempotentAndBoundedToKnownRecords(t *testing.T) {
	id := testSolutionID(t)
	if _, err := testService.PutSolutionRegistration(testCtx, backendWrite(id, "acme", "http://upstream.example:8080")); err != nil {
		t.Fatalf("register: %v", err)
	}
	first, err := testService.DeleteSolutionRegistration(testCtx, id, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	again, err := testService.DeleteSolutionRegistration(testCtx, id, nil)
	if err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if again.Revision != first.Revision {
		t.Fatalf("a repeated delete advanced the revision %d -> %d", first.Revision, again.Revision)
	}
	if _, err := testService.DeleteSolutionRegistration(testCtx, testSolutionID(t), nil); err != business.ErrSolutionRegistrationNotFound {
		t.Fatalf("err = %v, want not found", err)
	}
}

// A write must name exactly one half. The service rejects both-or-neither
// before anything reaches the record, so a malformed registrant cannot store a
// half it did not mean to claim.
func TestSolutionRegistry_RejectsWriteWithoutExactlyOneHalf(t *testing.T) {
	id := testSolutionID(t)
	both := frontendWrite(id, "acme", `{}`)
	both.Backend = &business.SolutionBackendRegistration{
		Upstream:     "http://upstream.example:8080",
		ServiceAlias: id,
	}
	if _, err := testService.PutSolutionRegistration(testCtx, both); err != business.ErrSolutionRegistrationHalfMissing {
		t.Fatalf("err = %v, want half missing", err)
	}
	neither := business.SolutionRegistrationWrite{
		SolutionID: id, Publisher: "acme", Lease: time.Minute,
	}
	if _, err := testService.PutSolutionRegistration(testCtx, neither); err != business.ErrSolutionRegistrationHalfMissing {
		t.Fatalf("err = %v, want half missing", err)
	}
}
