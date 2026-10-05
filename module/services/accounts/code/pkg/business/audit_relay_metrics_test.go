package business

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type auditQueueSnapshotFake struct {
	snapshot AuditQueueSnapshot
	err      error
}

func (f *auditQueueSnapshotFake) Snapshot(context.Context) (AuditQueueSnapshot, error) {
	return f.snapshot, f.err
}

// monitorFixture runs a monitor over a fake queue, a manual clock and a manual
// metric reader.
type monitorFixture struct {
	source  *auditQueueSnapshotFake
	reader  *sdkmetric.ManualReader
	monitor *AuditRelayMonitor
	now     time.Time
}

func newMonitorFixture(t *testing.T, interval time.Duration) *monitorFixture {
	t.Helper()
	f := &monitorFixture{
		source: &auditQueueSnapshotFake{},
		reader: sdkmetric.NewManualReader(),
		now:    time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	monitor, err := NewAuditRelayMonitor(f.source, provider.Meter("audit-relay-test"), interval)
	require.NoError(t, err)
	monitor.now = func() time.Time { return f.now }
	t.Cleanup(func() { require.NoError(t, monitor.Shutdown(t.Context())) })
	f.monitor = monitor
	return f
}

func (f *monitorFixture) collect(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var exported metricdata.ResourceMetrics
	require.NoError(t, f.reader.Collect(t.Context(), &exported))
	byName := map[string]metricdata.Metrics{}
	for _, scope := range exported.ScopeMetrics {
		for _, item := range scope.Metrics {
			byName[item.Name] = item
		}
	}
	return byName
}

// depth, age and quarantined read a gauge's single point, and report whether
// the series has one at all.
func (f *monitorFixture) depth(t *testing.T) (int64, bool) {
	t.Helper()
	return int64Point(t, f.collect(t), "saas.audit_queue.depth")
}

func (f *monitorFixture) quarantined(t *testing.T) (int64, bool) {
	t.Helper()
	return int64Point(t, f.collect(t), "saas.audit_queue.quarantined")
}

func (f *monitorFixture) age(t *testing.T) (float64, bool) {
	t.Helper()
	metric, ok := f.collect(t)["saas.audit_queue.oldest_age"]
	if !ok {
		return 0, false // an observable series nothing observed is not exported at all
	}
	points := metric.Data.(metricdata.Gauge[float64]).DataPoints
	if len(points) == 0 {
		return 0, false
	}
	require.Len(t, points, 1)
	return points[0].Value, true
}

func (f *monitorFixture) snapshotErrors(t *testing.T) int64 {
	t.Helper()
	metric, ok := f.collect(t)["saas.audit_queue.snapshot_errors"]
	require.True(t, ok, "failed reads of the queue are counted")
	var total int64
	for _, point := range metric.Data.(metricdata.Sum[int64]).DataPoints {
		total += point.Value
	}
	return total
}

func int64Point(t *testing.T, byName map[string]metricdata.Metrics, name string) (int64, bool) {
	t.Helper()
	metric, ok := byName[name]
	if !ok {
		return 0, false // an observable series nothing observed is not exported at all
	}
	points := metric.Data.(metricdata.Gauge[int64]).DataPoints
	if len(points) == 0 {
		return 0, false
	}
	require.Len(t, points, 1)
	return points[0].Value, true
}

func TestAuditRelayMonitorReportsLagUntilTheQueueClears(t *testing.T) {
	f := newMonitorFixture(t, time.Second)
	oldest := f.now.Add(-6 * time.Minute)
	f.source.snapshot = AuditQueueSnapshot{Depth: 7, OldestEnqueuedAt: &oldest, Quarantined: 2}

	require.NoError(t, f.monitor.RunOnce(t.Context()))
	depth, ok := f.depth(t)
	require.True(t, ok)
	require.Equal(t, int64(7), depth)
	age, ok := f.age(t)
	require.True(t, ok)
	require.Equal(t, 360.0, age)
	quarantined, ok := f.quarantined(t)
	require.True(t, ok)
	require.Equal(t, int64(2), quarantined, "rows set aside are reported until they are dealt with")

	f.source.snapshot = AuditQueueSnapshot{}
	require.NoError(t, f.monitor.RunOnce(t.Context()))
	depth, _ = f.depth(t)
	require.Equal(t, int64(0), depth)
	age, _ = f.age(t)
	require.Equal(t, 0.0, age)
	quarantined, _ = f.quarantined(t)
	require.Equal(t, int64(0), quarantined)
}

// A gauge that keeps its last value when the queue cannot be read reports a
// healthy relay while the relay is down: the series must go absent instead,
// and the failure be counted.
func TestAuditRelayMonitorDropsItsSeriesWhenTheLatestReadFailed(t *testing.T) {
	f := newMonitorFixture(t, time.Second)
	oldest := f.now.Add(-time.Minute)
	f.source.snapshot = AuditQueueSnapshot{Depth: 7, OldestEnqueuedAt: &oldest}
	require.NoError(t, f.monitor.RunOnce(t.Context()))
	_, ok := f.depth(t)
	require.True(t, ok)

	f.source.err = errors.New("database unavailable")
	require.ErrorContains(t, f.monitor.RunOnce(t.Context()), "database unavailable")
	_, ok = f.depth(t)
	require.False(t, ok, "no depth is reported for a queue that could not be read")
	_, ok = f.age(t)
	require.False(t, ok, "no age either: a stale zero would read as a drained queue")
	_, ok = f.quarantined(t)
	require.False(t, ok)
	require.Equal(t, int64(1), f.snapshotErrors(t))

	f.source.err = nil
	require.NoError(t, f.monitor.RunOnce(t.Context()))
	depth, ok := f.depth(t)
	require.True(t, ok, "the series returns with the next successful read")
	require.Equal(t, int64(7), depth)
	require.Equal(t, int64(1), f.snapshotErrors(t), "and the count of failures does not reset")
}

func TestAuditRelayMonitorCountsAnInconsistentSnapshotAsAFailure(t *testing.T) {
	f := newMonitorFixture(t, time.Second)
	f.source.snapshot = AuditQueueSnapshot{Depth: 3} // rows but no oldest row
	require.ErrorContains(t, f.monitor.RunOnce(t.Context()), "inconsistent")
	_, ok := f.depth(t)
	require.False(t, ok)
	require.Equal(t, int64(1), f.snapshotErrors(t))
}

// A monitor that stopped reading — hung, or its loop gone — must not leave its
// last reading standing: a reading is reported for twice the interval.
func TestAuditRelayMonitorDropsItsSeriesWhenTheReadIsStale(t *testing.T) {
	f := newMonitorFixture(t, 10*time.Second)
	oldest := f.now.Add(-time.Minute)
	f.source.snapshot = AuditQueueSnapshot{Depth: 4, OldestEnqueuedAt: &oldest}
	require.NoError(t, f.monitor.RunOnce(t.Context()))

	f.now = f.now.Add(20 * time.Second)
	depth, ok := f.depth(t)
	require.True(t, ok, "a reading two intervals old is still current")
	require.Equal(t, int64(4), depth)
	age, _ := f.age(t)
	require.Equal(t, 80.0, age, "and the age keeps rising between readings")

	f.now = f.now.Add(time.Second)
	_, ok = f.depth(t)
	require.False(t, ok, "past two intervals it is stale and reported as absent")
	_, ok = f.age(t)
	require.False(t, ok)
}

// The alert pack names the monitor's series in Prometheus's spelling. The names
// are what an alert in another repository is written against, so a series the
// pack mentions must be one the monitor exports, and every series the monitor
// exports must be watched by some alert.
func TestAuditQueueAlertsNameTheSeriesTheMonitorExports(t *testing.T) {
	f := newMonitorFixture(t, time.Second)
	f.source.err = errors.New("unreadable")
	require.Error(t, f.monitor.RunOnce(t.Context())) // creates the counter's series
	f.source.err = nil
	require.NoError(t, f.monitor.RunOnce(t.Context()))

	exported := map[string]string{}
	for name, item := range f.collect(t) {
		prom := strings.ReplaceAll(name, ".", "_")
		if item.Unit == "s" {
			prom += "_seconds"
		}
		if _, counter := item.Data.(metricdata.Sum[int64]); counter {
			prom += "_total"
		}
		exported[prom] = name
	}
	require.Equal(t, map[string]string{
		"saas_audit_queue_depth":                 "saas.audit_queue.depth",
		"saas_audit_queue_oldest_age_seconds":    "saas.audit_queue.oldest_age",
		"saas_audit_queue_quarantined":           "saas.audit_queue.quarantined",
		"saas_audit_queue_snapshot_errors_total": "saas.audit_queue.snapshot_errors",
	}, exported, "these four names are a contract with the alerts written against them")

	pack, err := os.ReadFile("../metrics/slo_pack.json")
	require.NoError(t, err)
	mentioned := map[string]bool{}
	for _, name := range regexp.MustCompile(`saas_audit_queue_[a-z_]+`).FindAllString(string(pack), -1) {
		mentioned[name] = true
		require.Contains(t, exported, name, "the alert pack reads a series the monitor does not export")
	}
	for name := range exported {
		require.True(t, mentioned[name], "%s is exported and watched by no alert", name)
	}
}
