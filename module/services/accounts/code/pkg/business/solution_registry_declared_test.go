package business

import (
	"errors"
	"testing"
	"time"
)

// The mixed window (issue #952), both halves.
//
// Until the runtimes stop self-registering, the reconciler and the heartbeat path
// both write solution_registrations, and the rule that makes landing this safe is
// asymmetric. These tests pin BOTH halves of it, because each half protects a
// different thing: the declared half stops a runtime overriding delivery, and the
// undeclared half is what lets this land at all — every record in every existing
// deployment is undeclared, and if their behaviour moved, every solution on every
// host would change the day this merged.
//
// The planner is where the rules live, on the record the heartbeat path has
// already row-locked, so they are decided here without a database. The end-to-end
// path is exercised in solution_host_bindings_integration_test.go.

func declaredRecord(id string, now time.Time) *SolutionRegistration {
	return &SolutionRegistration{
		SolutionID: id,
		Publisher:  solutionRegistrationPublisher(id),
		Revision:   4,
		Frontend: &SolutionFrontendHalf{
			Revision:       4,
			Manifest:       `{"id":"` + id + `","v":1}`,
			LeaseExpiresAt: now.Add(time.Minute),
		},
		Backend: &SolutionBackendHalf{
			Revision:       4,
			Upstream:       "http://crm-01.crm.svc:8080",
			ServiceAlias:   id,
			LeaseExpiresAt: now.Add(time.Minute),
		},
		UpdatedAt: now,
		Declared: &SolutionDeclaredBinding{
			BindingID:  "acme.test.crm",
			Generation: 4,
			Release:    "acme/crm@1.4.0",
		},
	}
}

// A heartbeat for a declared record may refresh liveness. Presence was declared,
// but whether the workload is answering is something only the workload knows, and
// a declared record whose lease never renewed would be permanently unroutable.
func TestPlanSolutionRegistration_DeclaredRecordAcceptsALeaseRenewal(t *testing.T) {
	now := time.Now().UTC()
	current := declaredRecord("crm", now)
	write := backendWrite("crm", solutionRegistrationPublisher("crm"), current.Backend.Upstream)

	next, changed, err := planSolutionRegistrationWrite(current, write, now)
	if err != nil {
		t.Fatalf("renewal on a declared record: %v", err)
	}
	if changed {
		t.Fatal("an identical heartbeat is a renewal, not a change")
	}
	if !next.Backend.LeaseExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("lease = %v, want it refreshed", next.Backend.LeaseExpiresAt)
	}
}

// The upstream address and the manifest are observations the document
// deliberately does not carry: an address frozen into a delivery document is a
// resolution result that was true on one cluster until something moved. A
// heartbeat owns both.
func TestPlanSolutionRegistration_DeclaredRecordAcceptsANewUpstreamAndManifest(t *testing.T) {
	now := time.Now().UTC()

	current := declaredRecord("crm", now)
	revision := current.Revision
	moved := backendWrite("crm", solutionRegistrationPublisher("crm"), "http://crm-02.crm.svc:8080")
	moved.ExpectedRevision = &revision
	next, changed, err := planSolutionRegistrationWrite(current, moved, now)
	if err != nil {
		t.Fatalf("new upstream on a declared record: %v", err)
	}
	if !changed || next.Backend.Upstream != "http://crm-02.crm.svc:8080" {
		t.Fatalf("upstream = %q changed=%v, want the reported address", next.Backend.Upstream, changed)
	}

	current = declaredRecord("crm", now)
	revision = current.Revision
	republished := frontendWrite("crm", solutionRegistrationPublisher("crm"), `{"id":"crm","v":2}`)
	republished.ExpectedRevision = &revision
	next, changed, err = planSolutionRegistrationWrite(current, republished, now)
	if err != nil {
		t.Fatalf("new manifest on a declared record: %v", err)
	}
	if !changed || next.Frontend.Manifest != `{"id":"crm","v":2}` {
		t.Fatalf("manifest = %q changed=%v, want the reported manifest", next.Frontend.Manifest, changed)
	}
}

// The release is the declaration's. A heartbeat carries no release field, so the
// rule is that the write path must not drop the declaration it found — which is
// exactly the kind of thing a `next := *current` refactor silently breaks.
func TestPlanSolutionRegistration_DeclaredRecordKeepsItsReleaseAndDeclaration(t *testing.T) {
	now := time.Now().UTC()
	for _, probe := range []struct {
		name  string
		write func(*SolutionRegistration) SolutionRegistrationWrite
	}{
		{"renewal", func(current *SolutionRegistration) SolutionRegistrationWrite {
			return backendWrite("crm", current.Publisher, current.Backend.Upstream)
		}},
		{"changed half", func(current *SolutionRegistration) SolutionRegistrationWrite {
			revision := current.Revision
			write := backendWrite("crm", current.Publisher, "http://crm-99.crm.svc:8080")
			write.ExpectedRevision = &revision
			return write
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			current := declaredRecord("crm", now)
			next, _, err := planSolutionRegistrationWrite(current, probe.write(current), now)
			if err != nil {
				t.Fatalf("%s: %v", probe.name, err)
			}
			if next.Declared == nil {
				t.Fatal("a heartbeat must not drop the declaration")
			}
			if next.Declared.Release != "acme/crm@1.4.0" {
				t.Fatalf("release = %q, want the declared release", next.Declared.Release)
			}
			if next.Declared.BindingID != "acme.test.crm" || next.Declared.Generation != 4 {
				t.Fatalf("declaration = %+v, want it unchanged", next.Declared)
			}
		})
	}
}

// Repointing the route is refused. The alias a declared record is addressed by IS
// its solution id — that is how the host routes /solutions/<id>/* and how the
// reconciler resolved the record from the document — so a heartbeat naming a
// different one is asking to be addressed as something delivery did not declare.
func TestPlanSolutionRegistration_DeclaredRecordRefusesARepointedRoute(t *testing.T) {
	now := time.Now().UTC()
	current := declaredRecord("crm", now)
	revision := current.Revision
	write := backendWrite("crm", current.Publisher, current.Backend.Upstream)
	write.Backend.ServiceAlias = "crm-takeover"
	write.ExpectedRevision = &revision

	_, _, err := planSolutionRegistrationWrite(current, write, now)
	if !errors.Is(err, ErrSolutionRegistrationDeclaredRoute) {
		t.Fatalf("err = %v, want ErrSolutionRegistrationDeclaredRoute", err)
	}
}

// A declared removal cannot be erased by a heartbeat, and — unlike an operator's
// deregistration — not even by one naming the tombstone's own revision. The next
// reconcile pass re-asserts the removal, so honouring the write would serve a
// solution delivery declared absent for exactly one interval.
func TestPlanSolutionRegistration_DeclaredTombstoneRefusesEveryHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	tombstoned := now
	current := &SolutionRegistration{
		SolutionID:   "crm",
		Publisher:    solutionRegistrationPublisher("crm"),
		Revision:     9,
		UpdatedAt:    now,
		TombstonedAt: &tombstoned,
		Declared: &SolutionDeclaredBinding{
			BindingID:  "acme.test.crm",
			Generation: 5,
			Release:    "acme/crm@1.4.0",
		},
	}

	bare := backendWrite("crm", current.Publisher, "http://crm-01.crm.svc:8080")
	if _, _, err := planSolutionRegistrationWrite(current, bare, now); !errors.Is(err, ErrSolutionRegistrationDeclaredWithdrawn) {
		t.Fatalf("bare heartbeat err = %v, want ErrSolutionRegistrationDeclaredWithdrawn", err)
	}

	revision := current.Revision
	reactivating := backendWrite("crm", current.Publisher, "http://crm-01.crm.svc:8080")
	reactivating.ExpectedRevision = &revision
	if _, _, err := planSolutionRegistrationWrite(current, reactivating, now); !errors.Is(err, ErrSolutionRegistrationDeclaredWithdrawn) {
		t.Fatalf("reactivating heartbeat err = %v, want ErrSolutionRegistrationDeclaredWithdrawn", err)
	}
}

// A declared record cannot create presence by heartbeat either, in the one shape
// that would: a runtime claiming a half on a record whose declaration removed it.
// The surviving tombstone is the refusal, and the previous test pins it; what this
// one pins is that an UNDECLARED tombstone still behaves as it did, because that
// is the path every solution on every existing host uses to re-register.
func TestPlanSolutionRegistration_UndeclaredTombstoneStillRevivesByRevision(t *testing.T) {
	now := time.Now().UTC()
	tombstoned := now
	current := &SolutionRegistration{
		SolutionID:   "crm",
		Publisher:    solutionRegistrationPublisher("crm"),
		Revision:     9,
		UpdatedAt:    now,
		TombstonedAt: &tombstoned,
	}
	revision := current.Revision
	write := backendWrite("crm", current.Publisher, "http://crm-01.crm.svc:8080")
	write.ExpectedRevision = &revision

	next, changed, err := planSolutionRegistrationWrite(current, write, now)
	if err != nil {
		t.Fatalf("undeclared reactivation: %v", err)
	}
	if !changed || next.TombstonedAt != nil || next.Backend == nil {
		t.Fatalf("undeclared reactivation must revive the record: %+v", next)
	}
}

// The undeclared half, stated as one test over every rule the declared half adds:
// none of them fires on a record with no declaration. This is the test that would
// fail if a future change made a declared-only rule unconditional, which is the
// change that would break every deployment on the day it merged.
func TestPlanSolutionRegistration_UndeclaredRecordIsUnaffectedByTheDeclaredRules(t *testing.T) {
	now := time.Now().UTC()
	base := func() *SolutionRegistration {
		record := declaredRecord("crm", now)
		record.Declared = nil
		return record
	}

	// A heartbeat renaming its own service alias: accepted, as before. The alias
	// is the registrant's to choose on an undeclared record.
	current := base()
	revision := current.Revision
	renamed := backendWrite("crm", current.Publisher, current.Backend.Upstream)
	renamed.Backend.ServiceAlias = "crm-internal"
	renamed.ExpectedRevision = &revision
	next, changed, err := planSolutionRegistrationWrite(current, renamed, now)
	if err != nil {
		t.Fatalf("undeclared alias change: %v", err)
	}
	if !changed || next.Backend.ServiceAlias != "crm-internal" {
		t.Fatalf("alias = %q changed=%v, want the registrant's alias", next.Backend.ServiceAlias, changed)
	}

	// A first claim on a record that does not exist: accepted, as before.
	if _, _, err := planSolutionRegistrationWrite(nil, backendWrite("crm", "solution:crm", "http://crm:8080"), now); err != nil {
		t.Fatalf("undeclared first claim: %v", err)
	}
}
