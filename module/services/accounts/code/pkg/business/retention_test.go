package business_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// retentionStoreFake records what RunRetention asks of the store, and whether
// each call ran inside a control-plane transaction. Its clock is the database's,
// and each part can be made to fail.
type retentionStoreFake struct {
	business.Store
	policies       []*business.RetentionPolicy
	databaseNow    time.Time
	clockErr       error
	ensureErr      error
	removeErr      map[string]error
	inControlPlane bool
	ensured        []bool
	cutoffs        map[string][]time.Time
	auditDrops     []bool
}

// retentionDatabaseNow is the database's clock in these tests: a fixed instant,
// hours away from the process's, with a fraction of a second, in a zone that
// keeps daylight saving time, so a cutoff counted from anything else, or in
// another zone, shows.
var retentionDatabaseNow = func() time.Time {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return time.Date(2024, time.March, 11, 0, 30, 0, 123456789, newYork)
}()

func newRetentionStoreFake(policies ...*business.RetentionPolicy) *retentionStoreFake {
	return &retentionStoreFake{
		policies:    policies,
		databaseNow: retentionDatabaseNow,
		removeErr:   map[string]error{},
		cutoffs:     map[string][]time.Time{},
	}
}

func (f *retentionStoreFake) GetRetentionPolicies(context.Context) ([]*business.RetentionPolicy, error) {
	return f.policies, nil
}

func (f *retentionStoreFake) DatabaseNow(context.Context) (time.Time, error) {
	return f.databaseNow, f.clockErr
}

func (f *retentionStoreFake) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	f.inControlPlane = true
	defer func() { f.inControlPlane = false }()
	return fn(ctx)
}

func (f *retentionStoreFake) EnsureAuditPartitions(context.Context, int) error {
	f.ensured = append(f.ensured, f.inControlPlane)
	return f.ensureErr
}

func (f *retentionStoreFake) remove(resource string, before time.Time) (int64, error) {
	f.cutoffs[resource] = append(f.cutoffs[resource], before)
	if err := f.removeErr[resource]; err != nil {
		return 0, err
	}
	return 1, nil
}

func (f *retentionStoreFake) DropAuditPartitionsBefore(_ context.Context, before time.Time) (int64, error) {
	f.auditDrops = append(f.auditDrops, f.inControlPlane)
	return f.remove("audit_events", before)
}

func (f *retentionStoreFake) DeleteOldSessions(_ context.Context, before time.Time) (int64, error) {
	return f.remove("sessions", before)
}

func (f *retentionStoreFake) DeleteOldWebhookDeliveries(_ context.Context, before time.Time) (int64, error) {
	return f.remove("webhook_deliveries", before)
}

func (f *retentionStoreFake) DeleteOldNotifications(_ context.Context, before time.Time) (int64, error) {
	return f.remove("notifications", before)
}

var retainedResourceTypes = []string{"audit_events", "sessions", "webhook_deliveries", "notifications"}

func retentionPolicies(days int) []*business.RetentionPolicy {
	var policies []*business.RetentionPolicy
	for _, resource := range retainedResourceTypes {
		policies = append(policies, &business.RetentionPolicy{ResourceType: resource, RetentionDays: days})
	}
	return policies
}

func runRetention(t *testing.T, store *retentionStoreFake) (map[string]int64, error) {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	return service.RunRetention(context.Background())
}

// retentionFailures lists a run's failed parts as "resource step".
func retentionFailures(t *testing.T, err error) []string {
	t.Helper()
	var run *business.RetentionError
	require.ErrorAs(t, err, &run, "a run that did not do all it should says so")
	var failed []string
	for _, failure := range run.Failures {
		require.Error(t, failure.Err)
		failed = append(failed, failure.Resource+" "+string(failure.Step))
	}
	return failed
}

// A window that is not a positive number of days is a policy no run can apply.
// Read literally, zero days reaches every record written before the run, and
// fewer reach records written after it, so the run removes nothing for it and
// reports the policy as a failure, rather than reading it as anything else.
// Audit partitions are still provisioned: writes need them whatever the window.
func TestRetentionRefusesAWindowThatIsNotAPositiveNumberOfDays(t *testing.T) {
	for _, days := range []int{0, -1, -365} {
		store := newRetentionStoreFake(retentionPolicies(days)...)
		deleted, err := runRetention(t, store)
		require.Equal(t, []string{"audit_events policy", "sessions policy", "webhook_deliveries policy", "notifications policy"},
			retentionFailures(t, err), "retention_days=%d is reported for every resource", days)
		require.Empty(t, store.cutoffs, "retention_days=%d removes nothing", days)
		require.Empty(t, deleted, "retention_days=%d reports nothing removed", days)
		require.Equal(t, []bool{true}, store.ensured, "audit partitions are provisioned whatever the window")
	}
}

// A window is counted back from the database's clock, in days of 24 hours, so
// its cutoff is exactly the window before the database's present however long
// the window is, including past the 106751 days a time.Duration can hold, and
// whatever this process's clock says. A window reaching back past the earliest
// instant a timestamp can hold has nothing older than it, and removes nothing.
func TestRetentionCountsEachWindowBackFromTheDatabaseClock(t *testing.T) {
	for _, days := range []int{1, 10, 365, 106751, 106752, 150000, 213503, 213504, 213600, 999999, 2_000_000, 9999999, 2147483647} {
		store := newRetentionStoreFake(retentionPolicies(days)...)
		_, err := runRetention(t, store)
		require.NoError(t, err)

		window := int64(days) * 24 * 60 * 60
		want := time.Unix(retentionDatabaseNow.Unix()-window, int64(retentionDatabaseNow.Nanosecond()))
		reachable := !want.Before(time.Date(-4713, time.November, 24, 0, 0, 0, 0, time.UTC))
		for _, resource := range retainedResourceTypes {
			cutoffs := store.cutoffs[resource]
			if !reachable {
				require.Empty(t, cutoffs, "retention_days=%d reaches past every record; %s removes nothing", days, resource)
				continue
			}
			require.Len(t, cutoffs, 1, "retention_days=%d: %s", days, resource)
			require.True(t, cutoffs[0].Equal(want),
				"retention_days=%d: the %s cutoff is %s, %d days of 24 hours before the database's clock, not %s",
				days, resource, want, days, cutoffs[0])
		}
	}
}

// A run goes on past a part that fails, and reports every failure together,
// with the counts of the parts that succeeded: one resource's lock wait or
// broken table does not stop the others' retention, nor hide behind their
// success.
func TestRetentionReportsEveryFailedPartAndGoesOn(t *testing.T) {
	store := newRetentionStoreFake(retentionPolicies(30)...)
	store.ensureErr = errors.New("provisioning waited too long")
	store.removeErr["audit_events"] = errors.New("drop waited too long")
	store.removeErr["sessions"] = errors.New("sessions unavailable")

	deleted, err := runRetention(t, store)
	require.Equal(t, []string{"audit_events provision", "audit_events remove", "sessions remove"}, retentionFailures(t, err))
	require.Equal(t, map[string]int64{"webhook_deliveries": 1, "notifications": 1}, deleted,
		"the resources that succeeded are counted, and only they")
	for _, resource := range retainedResourceTypes {
		require.Len(t, store.cutoffs[resource], 1, "%s was attempted", resource)
	}
}

// Without the database's clock no cutoff can be counted, so the run removes
// nothing and says why.
func TestRetentionRemovesNothingWithoutTheDatabaseClock(t *testing.T) {
	store := newRetentionStoreFake(retentionPolicies(30)...)
	store.clockErr = errors.New("database unavailable")
	deleted, err := runRetention(t, store)
	require.Equal(t, []string{"data_retention_policies load"}, retentionFailures(t, err))
	require.Empty(t, store.cutoffs)
	require.Empty(t, deleted)
}

// Audit writes need the months ahead to have partitions whatever the policies
// say, so a run provisions them with no audit_events policy at all.
func TestRetentionProvisionsAuditPartitionsWhateverThePolicies(t *testing.T) {
	store := newRetentionStoreFake(&business.RetentionPolicy{ResourceType: "sessions", RetentionDays: 90})
	_, err := runRetention(t, store)
	require.NoError(t, err)
	require.Equal(t, []bool{true}, store.ensured, "partitions are provisioned, in a control-plane transaction")
	require.Empty(t, store.auditDrops, "with no audit_events policy, nothing is dropped")
}

// The audit drop decides which partitions are due once it holds its lock, from
// the catalog as it stands then, which takes a transaction of its own: the drop
// runs outside the control-plane transaction that provisions partitions, and the
// store opens it at READ COMMITTED.
func TestRetentionDropsAuditPartitionsOutsideTheProvisioningTransaction(t *testing.T) {
	store := newRetentionStoreFake(&business.RetentionPolicy{ResourceType: "audit_events", RetentionDays: 365})
	_, err := runRetention(t, store)
	require.NoError(t, err)
	require.Equal(t, []bool{true}, store.ensured, "partitions are provisioned in a control-plane transaction")
	require.Equal(t, []bool{false}, store.auditDrops, "the drop runs in a transaction of its own")
}
