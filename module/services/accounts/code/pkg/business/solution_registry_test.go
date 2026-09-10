package business

import (
	"testing"
	"time"
)

// The registry's compare-and-swap, tombstone, and lease rules decided here are
// what both surfaces depend on, so they are exercised twice: once against the
// pure planner, where every branch is reachable without a database, and once
// end to end against Postgres, where the CHECK constraints and the revision
// sequence are the things actually under test.

func frontendWrite(id, publisher, manifest string) SolutionRegistrationWrite {
	return SolutionRegistrationWrite{
		SolutionID: id,
		Publisher:  publisher,
		Lease:      time.Minute,
		Frontend:   &SolutionFrontendRegistration{Manifest: manifest},
	}
}

func backendWrite(id, publisher, upstream string) SolutionRegistrationWrite {
	return SolutionRegistrationWrite{
		SolutionID: id,
		Publisher:  publisher,
		Lease:      time.Minute,
		Backend: &SolutionBackendRegistration{
			Upstream:     upstream,
			ServiceAlias: id,
		},
	}
}

func TestPlanSolutionRegistration_FirstClaimNeedsNoRevision(t *testing.T) {
	now := time.Now().UTC()
	next, changed, err := planSolutionRegistrationWrite(nil, frontendWrite("demo", "acme", `{"id":"demo"}`), now)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !changed {
		t.Fatal("first claim must advance the record")
	}
	if next.Frontend == nil || next.Backend != nil {
		t.Fatal("first claim must set only the half it carried")
	}
	if !next.Frontend.LeaseExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("lease = %v, want %v", next.Frontend.LeaseExpiresAt, now.Add(time.Minute))
	}
}

// A publisher holding a revision for a record that no longer exists is working
// from a view that has been deleted out from under it; recreating the record
// silently would be a resurrection by another name.
func TestPlanSolutionRegistration_RevisionAgainstAbsentRecordIsStale(t *testing.T) {
	stale := int64(7)
	write := frontendWrite("demo", "acme", `{"id":"demo"}`)
	write.ExpectedRevision = &stale
	if _, _, err := planSolutionRegistrationWrite(nil, write, time.Now().UTC()); err != ErrSolutionRegistrationStale {
		t.Fatalf("err = %v, want stale", err)
	}
}

func TestPlanSolutionRegistration_ForeignPublisherRefused(t *testing.T) {
	current := &SolutionRegistration{SolutionID: "demo", Publisher: "acme", Revision: 3}
	_, _, err := planSolutionRegistrationWrite(current, frontendWrite("demo", "other", `{}`), time.Now().UTC())
	if err != ErrSolutionPublisherMismatch {
		t.Fatalf("err = %v, want publisher mismatch", err)
	}
}

// Replacing a half that already holds different content is the case that needs
// a token; claiming a half the record does not hold yet is not.
func TestPlanSolutionRegistration_ReplacingHalfRequiresRevision(t *testing.T) {
	current := &SolutionRegistration{
		SolutionID: "demo", Publisher: "acme", Revision: 3,
		Frontend: &SolutionFrontendHalf{Revision: 3, Manifest: `{"v":1}`, LeaseExpiresAt: time.Now().Add(time.Hour)},
	}
	_, _, err := planSolutionRegistrationWrite(current, frontendWrite("demo", "acme", `{"v":2}`), time.Now().UTC())
	if err != ErrSolutionRegistrationRevisionRequired {
		t.Fatalf("err = %v, want revision required", err)
	}

	claimOther := backendWrite("demo", "acme", "http://upstream.example:8080")
	if _, changed, err := planSolutionRegistrationWrite(current, claimOther, time.Now().UTC()); err != nil || !changed {
		t.Fatalf("claiming the unheld half: changed=%v err=%v", changed, err)
	}
}

func TestPlanSolutionRegistration_StaleRevisionRefused(t *testing.T) {
	current := &SolutionRegistration{
		SolutionID: "demo", Publisher: "acme", Revision: 9,
		Frontend: &SolutionFrontendHalf{Revision: 9, Manifest: `{"v":1}`, LeaseExpiresAt: time.Now().Add(time.Hour)},
	}
	behind := int64(4)
	write := frontendWrite("demo", "acme", `{"v":2}`)
	write.ExpectedRevision = &behind
	if _, _, err := planSolutionRegistrationWrite(current, write, time.Now().UTC()); err != ErrSolutionRegistrationStale {
		t.Fatalf("err = %v, want stale", err)
	}
}

// A heartbeat re-sends identical content. It must refresh the lease without
// advancing the revision, or every replica's cache churns at heartbeat cadence.
func TestPlanSolutionRegistration_IdenticalContentRenewsWithoutAdvancing(t *testing.T) {
	now := time.Now().UTC()
	current := &SolutionRegistration{
		SolutionID: "demo", Publisher: "acme", Revision: 5,
		Frontend: &SolutionFrontendHalf{Revision: 5, Manifest: `{"v":1}`, LeaseExpiresAt: now.Add(time.Second)},
	}
	next, changed, err := planSolutionRegistrationWrite(current, frontendWrite("demo", "acme", `{"v":1}`), now)
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if changed {
		t.Fatal("a renewal must not advance the revision")
	}
	if !next.Frontend.LeaseExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("lease not extended: %v", next.Frontend.LeaseExpiresAt)
	}
	if current.Frontend.LeaseExpiresAt != now.Add(time.Second) {
		t.Fatal("planning mutated the record it was given")
	}
}

// The resurrection rule: a retiring deployment's delayed retry carries either no
// token or its pre-deletion one, and both must lose to the tombstone.
func TestPlanSolutionRegistration_TombstoneBlocksResurrection(t *testing.T) {
	tombstoned := time.Now().UTC()
	current := &SolutionRegistration{
		SolutionID: "demo", Publisher: "acme", Revision: 11, TombstonedAt: &tombstoned,
	}
	if _, _, err := planSolutionRegistrationWrite(current, frontendWrite("demo", "acme", `{}`), tombstoned); err != ErrSolutionRegistrationTombstoned {
		t.Fatalf("err = %v, want tombstoned", err)
	}

	preDeletion := int64(10)
	retry := frontendWrite("demo", "acme", `{}`)
	retry.ExpectedRevision = &preDeletion
	if _, _, err := planSolutionRegistrationWrite(current, retry, tombstoned); err != ErrSolutionRegistrationStale {
		t.Fatalf("err = %v, want stale", err)
	}

	// A deliberate re-registration names the tombstone's own revision.
	reactivate := frontendWrite("demo", "acme", `{}`)
	reactivate.ExpectedRevision = &current.Revision
	next, changed, err := planSolutionRegistrationWrite(current, reactivate, tombstoned)
	if err != nil || !changed {
		t.Fatalf("reactivation: changed=%v err=%v", changed, err)
	}
	if next.TombstonedAt != nil || next.Frontend == nil {
		t.Fatal("reactivation must clear the tombstone and set the half")
	}
}

func TestSolutionRegistrationStatus(t *testing.T) {
	now := time.Now().UTC()
	live := now.Add(time.Hour)
	lapsed := now.Add(-time.Second)
	tombstoned := now

	for _, tc := range []struct {
		name   string
		record SolutionRegistration
		want   SolutionRegistrationStatus
	}{
		{
			name:   "one half is pending, never active",
			record: SolutionRegistration{Frontend: &SolutionFrontendHalf{LeaseExpiresAt: live}},
			want:   SolutionRegistrationPending,
		},
		{
			name: "both halves live",
			record: SolutionRegistration{
				Frontend: &SolutionFrontendHalf{LeaseExpiresAt: live},
				Backend:  &SolutionBackendHalf{LeaseExpiresAt: live},
			},
			want: SolutionRegistrationActive,
		},
		{
			name: "either lease lapsed",
			record: SolutionRegistration{
				Frontend: &SolutionFrontendHalf{LeaseExpiresAt: live},
				Backend:  &SolutionBackendHalf{LeaseExpiresAt: lapsed},
			},
			want: SolutionRegistrationExpired,
		},
		{
			name: "contract mismatch outranks a lapsed lease",
			record: SolutionRegistration{
				Frontend: &SolutionFrontendHalf{LeaseExpiresAt: lapsed, ContractVersion: "v1"},
				Backend:  &SolutionBackendHalf{LeaseExpiresAt: lapsed, ContractVersion: "v2"},
			},
			want: SolutionRegistrationIncompatible,
		},
		{
			name: "a tombstone outranks everything",
			record: SolutionRegistration{
				TombstonedAt: &tombstoned,
				Frontend:     &SolutionFrontendHalf{LeaseExpiresAt: live},
				Backend:      &SolutionBackendHalf{LeaseExpiresAt: live},
			},
			want: SolutionRegistrationTombstoned,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.record.Status(now); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}
