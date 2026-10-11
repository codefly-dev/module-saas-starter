package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// retentionStoreFake records what RunRetention asks of the store, and whether
// each call ran inside a control-plane transaction.
type retentionStoreFake struct {
	business.Store
	policies       []*business.RetentionPolicy
	inControlPlane bool
	ensured        []bool
	cutoffs        map[string][]time.Time
	auditDrops     []bool
}

func newRetentionStoreFake(policies ...*business.RetentionPolicy) *retentionStoreFake {
	return &retentionStoreFake{policies: policies, cutoffs: map[string][]time.Time{}}
}

func (f *retentionStoreFake) GetRetentionPolicies(context.Context) ([]*business.RetentionPolicy, error) {
	return f.policies, nil
}

func (f *retentionStoreFake) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	f.inControlPlane = true
	defer func() { f.inControlPlane = false }()
	return fn(ctx)
}

func (f *retentionStoreFake) EnsureAuditPartitions(context.Context, int) error {
	f.ensured = append(f.ensured, f.inControlPlane)
	return nil
}

func (f *retentionStoreFake) DropAuditPartitionsBefore(_ context.Context, before time.Time) (int64, error) {
	f.cutoffs["audit_events"] = append(f.cutoffs["audit_events"], before)
	f.auditDrops = append(f.auditDrops, f.inControlPlane)
	return 0, nil
}

func (f *retentionStoreFake) DeleteOldSessions(_ context.Context, before time.Time) (int64, error) {
	f.cutoffs["sessions"] = append(f.cutoffs["sessions"], before)
	return 0, nil
}

func (f *retentionStoreFake) DeleteOldWebhookDeliveries(_ context.Context, before time.Time) (int64, error) {
	f.cutoffs["webhook_deliveries"] = append(f.cutoffs["webhook_deliveries"], before)
	return 0, nil
}

func (f *retentionStoreFake) DeleteOldNotifications(_ context.Context, before time.Time) (int64, error) {
	f.cutoffs["notifications"] = append(f.cutoffs["notifications"], before)
	return 0, nil
}

var retainedResourceTypes = []string{"audit_events", "sessions", "webhook_deliveries", "notifications"}

func runRetention(t *testing.T, store *retentionStoreFake) (map[string]int64, time.Time, time.Time) {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	start := time.Now()
	deleted, err := service.RunRetention(context.Background())
	require.NoError(t, err)
	return deleted, start, time.Now()
}

// A window that is not a positive number of days removes nothing. Read
// literally, zero days reaches every record written before the run, and fewer
// reach records written after it; neither is a period anyone keeps data for.
// Audit partitions are still provisioned: writes need them whatever the window.
func TestRetentionRemovesNothingForAWindowThatIsNotAPositiveNumberOfDays(t *testing.T) {
	for _, days := range []int{0, -1, -365} {
		var policies []*business.RetentionPolicy
		for _, resource := range retainedResourceTypes {
			policies = append(policies, &business.RetentionPolicy{ResourceType: resource, RetentionDays: days})
		}
		store := newRetentionStoreFake(policies...)
		deleted, _, _ := runRetention(t, store)
		require.Empty(t, store.cutoffs, "retention_days=%d removes nothing", days)
		require.Empty(t, deleted, "retention_days=%d reports nothing removed", days)
		require.Equal(t, []bool{true}, store.ensured, "audit partitions are provisioned whatever the window")
	}
}

// A window is counted back from the run by the calendar, so however long it is,
// the cutoff is before the run. A window reaching back past the earliest instant
// a timestamp can hold has nothing older than it, and removes nothing.
func TestRetentionCutoffIsAlwaysBeforeTheRun(t *testing.T) {
	for _, days := range []int{1, 365, 106751, 106752, 150000, 213503, 213504, 213600, 999999, 2_000_000, 9999999, 2147483647} {
		var policies []*business.RetentionPolicy
		for _, resource := range retainedResourceTypes {
			policies = append(policies, &business.RetentionPolicy{ResourceType: resource, RetentionDays: days})
		}
		store := newRetentionStoreFake(policies...)
		_, start, end := runRetention(t, store)

		// A day either side absorbs a zone's offset changing across the window.
		earliest, latest := start.AddDate(0, 0, -days-1), end.AddDate(0, 0, -days+1)
		reachable := !start.AddDate(0, 0, -days).Before(time.Date(-4713, time.November, 24, 0, 0, 0, 0, time.UTC))
		for _, resource := range retainedResourceTypes {
			cutoffs := store.cutoffs[resource]
			if !reachable {
				require.Empty(t, cutoffs, "retention_days=%d reaches past every record; %s removes nothing", days, resource)
				continue
			}
			require.Len(t, cutoffs, 1, "retention_days=%d: %s", days, resource)
			cutoff := cutoffs[0]
			require.True(t, cutoff.Before(start), "retention_days=%d: the %s cutoff %s is before the run", days, resource, cutoff)
			require.False(t, cutoff.Before(earliest) || cutoff.After(latest),
				"retention_days=%d: the %s cutoff %s is %d days before the run", days, resource, cutoff, days)
		}
	}
}

// The audit drop decides which partitions are due once it holds its lock, from
// the catalog as it stands then, which takes a transaction of its own: the drop
// runs outside the control-plane transaction that provisions partitions, and the
// store opens it at READ COMMITTED.
func TestRetentionDropsAuditPartitionsOutsideTheProvisioningTransaction(t *testing.T) {
	store := newRetentionStoreFake(&business.RetentionPolicy{ResourceType: "audit_events", RetentionDays: 365})
	runRetention(t, store)
	require.Equal(t, []bool{true}, store.ensured, "partitions are provisioned in a control-plane transaction")
	require.Equal(t, []bool{false}, store.auditDrops, "the drop runs in a transaction of its own")
}
