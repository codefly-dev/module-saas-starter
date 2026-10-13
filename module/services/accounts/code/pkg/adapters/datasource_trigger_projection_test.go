package adapters

import (
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// EVERY FIELD OF THE DECLARATION REACHES THE WIRE (issue #1121).
//
// This test exists because a field that was never projected shipped: the
// declaration gained `HostSweepEnabled` and `HostSweepDueAt`, the panel was
// written to read them, and `triggerDeclarationToProto` was not updated — so
// the page would have told every operator that this host fires nothing, which
// is the opposite of the truth and exactly the claim the change was made to
// correct. Nothing caught it, because the panel's container test serves its own
// JSON fixture and never crosses this projection.
//
// So the assertion is deliberately field-by-field rather than "the important
// ones": a projection is a place where a value is silently dropped, and the
// only test that notices is one that names everything.
func TestTriggerDeclarationProjectionCarriesEveryField(t *testing.T) {
	last := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	next := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	due := time.Date(2026, 10, 12, 9, 7, 0, 0, time.UTC)
	declaration := business.SourceTriggerDeclaration{
		Release:   business.DatasourceReconcileRelease,
		Operation: business.DatasourceReconcileOperation,
		Schedule: business.DeclaredTriggerSchedule{
			Cron: "*/30 * * * *", Timezone: "UTC", MissedRun: "skip", Overlap: "skip",
			Deadline: 10 * time.Minute, Input: []byte(`{"org_id":"o","source_id":"s"}`),
		},
		Identity:         business.DeclaredTriggerIdentity{Source: "a-source"},
		IdentityHolder:   "module-a",
		Digest:           "4f1c",
		LastOccurrenceAt: &last,
		NextOccurrenceAt: &next,
		HostSweepEnabled: true,
		HostSweepDueAt:   &due,
	}

	out := triggerDeclarationToProto(declaration)

	require.Equal(t, business.DatasourceReconcileRelease, out.GetRelease())
	require.Equal(t, business.DatasourceReconcileOperation, out.GetOperation())
	require.Equal(t, "4f1c", out.GetDigest())
	require.Equal(t, "*/30 * * * *", out.GetSchedule().GetCron())
	require.Equal(t, "UTC", out.GetSchedule().GetTimezone())
	require.Equal(t, "skip", out.GetSchedule().GetMissedRun())
	require.Equal(t, "skip", out.GetSchedule().GetOverlap())
	require.Equal(t, int64(10*time.Minute), out.GetSchedule().GetDeadline())
	require.JSONEq(t, `{"org_id":"o","source_id":"s"}`, out.GetSchedule().GetInputJson())
	require.Equal(t, "a-source", out.GetIdentity().GetSource())
	require.Empty(t, out.GetIdentity().GetBinding())
	// The holder is NOT the executor, and the field's own comment says so; what
	// matters here is that a reader is given it at all (issue #1121, F1).
	require.Equal(t, "module-a", out.GetIdentityHolder())
	require.Equal(t, last, out.GetLastOccurrenceAt().AsTime())
	require.Equal(t, next, out.GetNextOccurrenceAt().AsTime())
	// What THIS HOST will do about the schedule, which is a different fact from
	// the cron above and the reason both are carried.
	require.True(t, out.GetHostSweepEnabled())
	require.Equal(t, due, out.GetHostSweepDueAt().AsTime())
	require.NotEqual(t, out.GetNextOccurrenceAt().AsTime(), out.GetHostSweepDueAt().AsTime(),
		"fixture: the declared instant and the swept instant must differ, which is the finding they report")
}

// A source that declares no trigger still says what this host is doing, because
// the stopgap sweeps a source whose cron could not be spelled just the same.
// The schedule and identity are withheld; the sweep's state is not.
func TestAnUndeclarableSourceStillReportsTheHostSweep(t *testing.T) {
	due := time.Date(2026, 10, 12, 9, 7, 0, 0, time.UTC)
	out := triggerDeclarationToProto(business.SourceTriggerDeclaration{
		Release:            business.DatasourceReconcileRelease,
		Operation:          business.DatasourceReconcileOperation,
		UndeclarableReason: business.TriggerUndeclarableInterval,
		HostSweepEnabled:   true,
		HostSweepDueAt:     &due,
	})

	require.Equal(t, business.TriggerUndeclarableInterval, out.GetUndeclarableReason())
	require.Nil(t, out.GetSchedule(), "an undeclarable source has no schedule to project")
	require.Nil(t, out.GetIdentity())
	require.True(t, out.GetHostSweepEnabled())
	require.Equal(t, due, out.GetHostSweepDueAt().AsTime())
}

// And with the switch off there is no instant to report: an operator must not
// be shown a pass this host will not take.
func TestASweepThatIsOffNamesNoInstant(t *testing.T) {
	out := triggerDeclarationToProto(business.SourceTriggerDeclaration{
		Release:          business.DatasourceReconcileRelease,
		Operation:        business.DatasourceReconcileOperation,
		Schedule:         business.DeclaredTriggerSchedule{Cron: "*/30 * * * *"},
		HostSweepEnabled: false,
	})

	require.False(t, out.GetHostSweepEnabled())
	require.Nil(t, out.GetHostSweepDueAt())
}
