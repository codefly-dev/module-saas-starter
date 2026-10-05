package auditops

import (
	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var probeNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func probeOptions() qualificationOptions {
	return qualificationOptions{orgs: organizations{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, runID: uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc"), from: probeNow, to: probeNow.Add(5 * time.Minute), timeout: time.Minute,
		clock: func() time.Time { return probeNow.Add(2 * time.Second) }}
}

type probeDB struct {
	exists bool
	count  int64
	// rows are the created_at of the audit_events rows the database holds; the
	// count of an interval is how many fall in [from, to).
	rows   []time.Time
	calls  [][2]time.Time
	writes []business.AuditEntry
}

func (d *probeDB) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	n := len(d.writes)
	err := fn(ctx)
	if err != nil {
		d.writes = d.writes[:n]
	}
	return err
}
func (d *probeDB) OrganizationIDExists(context.Context, string) (bool, error) { return d.exists, nil }
func (d *probeDB) CountAuditHistory(_ context.Context, from, to time.Time) (map[string]int64, error) {
	d.calls = append(d.calls, [2]time.Time{from, to})
	n := d.count
	for _, at := range d.rows {
		if !at.Before(from) && at.Before(to) {
			n++
		}
	}
	return map[string]int64{"": n}, nil
}
func (d *probeDB) RecordTx(_ context.Context, e business.AuditEntry) error {
	d.writes = append(d.writes, e)
	return nil
}

type probeWarehouse struct {
	business.AuditStore
	records []business.AuditRecord
	corrupt bool
	leak    bool
}

func (s *probeWarehouse) ReadStoredAuditEvents(_ context.Context, _, _ time.Time, visit func(business.StoredAuditEvent) error) error {
	for _, r := range s.records {
		event := business.StoredAuditEvent{DeploymentID: "deployment-1", Entry: r.Entry, Retention: r.Retention, DetailsSHA256: r.DetailsSHA256, Details: r.Details, HasDetails: true}
		if s.corrupt {
			event.Details = "{}"
		}
		if err := visit(event); err != nil {
			return err
		}
	}
	return nil
}
func (s *probeWarehouse) ListAuditEvents(_ context.Context, read business.AuditRead) ([]business.AuditEntry, string, error) {
	if err := read.Validate(); err != nil {
		return nil, "", err
	}
	var entries []business.AuditEntry
	for _, r := range s.records {
		if read.Scope.Platform() || r.Entry.OrgID == read.Scope.OrgID() || s.leak {
			entries = append(entries, r.Entry)
		}
	}
	return entries, "", nil
}
func (s *probeWarehouse) ExportAuditEvents(ctx context.Context, read business.AuditRead) ([]business.AuditEntry, error) {
	v, _, err := s.ListAuditEvents(ctx, read)
	return v, err
}
func (s *probeWarehouse) AggregateAuditEvents(ctx context.Context, read business.AuditRead, _ business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	v, _, err := s.ListAuditEvents(ctx, read)
	counts := map[string]float64{}
	for _, e := range v {
		counts[string(e.EventType)]++
	}
	var buckets []business.AuditAggregateBucket
	for typ, n := range counts {
		buckets = append(buckets, business.AuditAggregateBucket{Key: typ, Keys: []string{typ}, Metrics: map[string]float64{"count": n}})
	}
	return buckets, err
}
func (s *probeWarehouse) LatestSourceSyncEvents(_ context.Context, scope business.AuditReadScope, ids []string) (map[string]business.AuditSourceSyncEvent, error) {
	out := map[string]business.AuditSourceSyncEvent{}
	for _, r := range s.records {
		for _, id := range ids {
			if id == r.Entry.ID && r.Entry.OrgID == scope.OrgID() && r.Retention == business.RetentionContent {
				out[id] = business.AuditSourceSyncEvent{RequestedAt: r.Entry.CreatedAt}
			}
		}
	}
	return out, nil
}
func probeReceipt(t *testing.T, o qualificationOptions, records []business.AuditRecord) QualificationReceipt {
	t.Helper()
	var stderr bytes.Buffer
	_, swap, code := parse(nil, env(swapEnv), &stderr, probeNow)
	require.Zero(t, code)
	return newQualificationReceipt(o, swap, records)
}
func TestQualificationCommitsOnlyIntoExistingOrganizationsAndRefusesNewPostgresRows(t *testing.T) {
	o := probeOptions()
	records, err := qualificationRecords(o)
	require.NoError(t, err)
	r := probeReceipt(t, o, records)
	db := &probeDB{exists: false}
	require.Error(t, prepareQualification(context.Background(), o, db, db, records, &r))
	require.Empty(t, db.writes)
	db.exists = true
	db.count = 1
	require.Error(t, prepareQualification(context.Background(), o, db, db, records, &r))
	require.Empty(t, db.writes, "failed acceptance aborts fixture transaction")
	db.count = 0
	require.NoError(t, prepareQualification(context.Background(), o, db, db, records, &r))
	require.Len(t, db.writes, 6)
	require.Equal(t, "passed", r.Checks["transaction_queue"])
	require.Equal(t, "pending", r.Checks["warehouse_readback"])
}

// postgres_no_new_rows vouches for one interval of audit_events.created_at and
// the receipt names it. Prepare starts the interval and checks it up to the end
// of its fixture transaction; verify checks from that same start to its own run
// time, wherever that falls against the five-minute event window.
func TestQualificationPostgresCheckCoversExactlyTheIntervalItReports(t *testing.T) {
	prepare := func(t *testing.T, db *probeDB, o qualificationOptions) (QualificationReceipt, error) {
		records, err := qualificationRecords(o)
		require.NoError(t, err)
		r := probeReceipt(t, o, records)
		return r, prepareQualification(context.Background(), o, db, db, records, &r)
	}
	t.Run("prepare checks from its start to the end of its fixture transaction", func(t *testing.T) {
		db := &probeDB{exists: true}
		o := probeOptions()
		r, err := prepare(t, db, o)
		require.NoError(t, err)
		require.Equal(t, "passed", r.Checks["postgres_no_new_rows"])
		require.Equal(t, [][2]time.Time{{probeNow, probeNow.Add(2 * time.Second)}}, db.calls,
			"the interval read is the one from the start to now, not the five-minute window ahead")
		require.Equal(t, probeNow, *r.PostgresCheckedFrom)
		require.Equal(t, probeNow.Add(2*time.Second), *r.PostgresCheckedTo)
	})
	t.Run("a row created in that interval fails prepare and aborts its fixtures", func(t *testing.T) {
		db := &probeDB{exists: true, rows: []time.Time{probeNow.Add(time.Second)}}
		r, err := prepare(t, db, probeOptions())
		require.Error(t, err)
		require.Equal(t, "failed", r.Checks["transaction_queue"])
		require.Empty(t, db.writes)
	})
	t.Run("the interval is half open: before its start and at its end are outside it", func(t *testing.T) {
		db := &probeDB{exists: true, rows: []time.Time{probeNow.Add(-time.Second), probeNow.Add(2 * time.Second)}}
		_, err := prepare(t, db, probeOptions())
		require.NoError(t, err)
	})
	t.Run("a clock that has not moved leaves nothing to check and fails", func(t *testing.T) {
		o := probeOptions()
		o.clock = func() time.Time { return probeNow }
		_, err := prepare(t, &probeDB{exists: true}, o)
		require.ErrorContains(t, err, "interval is empty")
	})

	records, err := qualificationRecords(probeOptions())
	require.NoError(t, err)
	verify := func(t *testing.T, db *probeDB, at time.Duration) (QualificationReceipt, error) {
		o := probeOptions()
		o.clock = func() time.Time { return probeNow.Add(at) }
		store := &probeWarehouse{records: records}
		r := probeReceipt(t, o, records)
		return r, verifyQualification(context.Background(), o, db, store, store, records, &r)
	}
	t.Run("verify after the event window checks all the way to its own run time", func(t *testing.T) {
		db := &probeDB{exists: true}
		r, err := verify(t, db, 20*time.Minute)
		require.NoError(t, err)
		require.Equal(t, probeNow, *r.PostgresCheckedFrom)
		require.Equal(t, probeNow.Add(20*time.Minute), *r.PostgresCheckedTo, "past window_to, and reported as such")
		require.True(t, r.PostgresCheckedTo.After(r.WindowTo))
	})
	t.Run("a row after the five-minute window but before verify ran fails verify", func(t *testing.T) {
		r, err := verify(t, &probeDB{exists: true, rows: []time.Time{probeNow.Add(10 * time.Minute)}}, 20*time.Minute)
		require.Error(t, err)
		require.Equal(t, "failed", r.Checks["postgres_no_new_rows"])
		require.Equal(t, probeNow.Add(20*time.Minute), *r.PostgresCheckedTo)
	})
	t.Run("verify inside the window claims only the part of it that has happened", func(t *testing.T) {
		r, err := verify(t, &probeDB{exists: true}, time.Minute)
		require.NoError(t, err)
		require.Equal(t, "passed", r.Checks["postgres_no_new_rows"])
		require.Equal(t, probeNow.Add(time.Minute), *r.PostgresCheckedTo)
		require.True(t, r.PostgresCheckedTo.Before(r.WindowTo), "the receipt does not claim the window's future")
	})
}

func TestQualificationChecksHashesAndOrganizationIsolationWithoutClaimingOtherAcceptance(t *testing.T) {
	o := probeOptions()
	records, err := qualificationRecords(o)
	require.NoError(t, err)
	db := &probeDB{exists: true}
	store := &probeWarehouse{records: records}
	r := probeReceipt(t, o, records)
	require.NoError(t, verifyQualification(context.Background(), o, db, store, store, records, &r))
	for _, key := range []string{"warehouse_readback", "scoped_list", "aggregation", "export", "readable_sources", "organization_isolation"} {
		require.Equal(t, "passed", r.Checks[key])
	}
	for _, key := range []string{"deployment_isolation", "archive_lock_enforcement", "relay_interruption_recovery", "gateway_authorization", "alert_delivery"} {
		require.Equal(t, "pending", r.Checks[key])
	}
	store.corrupt = true
	r = probeReceipt(t, o, records)
	require.Error(t, verifyQualification(context.Background(), o, db, store, store, records, &r))
	require.Equal(t, "failed", r.Checks["warehouse_readback"])
	store.corrupt = false
	store.leak = true
	r = probeReceipt(t, o, records)
	require.Error(t, verifyQualification(context.Background(), o, db, store, store, records, &r))
	require.Equal(t, "failed", r.Checks["organization_isolation"])
	store.leak = false
	store.records = store.records[:5]
	require.ErrorIs(t, warehouseProbe(context.Background(), o, store, records, "deployment-1"), errFixturesPending)
	require.Error(t, warehouseProbe(context.Background(), o, &probeWarehouse{records: records}, records, "other-deployment"))
}
func TestQualificationBoundsAndDeterministicReceiptFixtures(t *testing.T) {
	o := probeOptions()
	records, err := qualificationRecords(o)
	require.NoError(t, err)
	again, err := qualificationRecords(o)
	require.NoError(t, err)
	require.Equal(t, records, again)
	other := o
	other.runID = uuid.New()
	newRun, err := qualificationRecords(other)
	require.NoError(t, err)
	require.NotEqual(t, records[0].Entry.ID, newRun[0].Entry.ID)
	args := []string{"-run-id", o.runID.String(), "-organization", o.orgs[0], "-organization", o.orgs[1], "-verify", "-window-from", o.from.Format(time.RFC3339Nano), "-window-to", o.to.Format(time.RFC3339Nano)}
	var stderr bytes.Buffer
	_, _, err = parseQualification(args, &stderr, probeNow)
	require.NoError(t, err)
	_, _, err = parseQualification(append(args, "-timeout", "6m"), &stderr, probeNow)
	require.Error(t, err)
	_, _, err = parseQualification(append(args, "-prepare"), &stderr, probeNow)
	require.Error(t, err)
	var out bytes.Buffer
	r := probeReceipt(t, o, records)
	r.ErrorCode = "qualification_failed"
	require.NoError(t, jsonEncode(&out, r))
	require.NotContains(t, out.String(), "method")
	require.NotContains(t, out.String(), "repo")
	require.NotContains(t, out.String(), "postgres://")
}
func jsonEncode(out *bytes.Buffer, v any) error { return json.NewEncoder(out).Encode(v) }

func TestQueueDrainedRequiresAnObservedEmptyQueue(t *testing.T) {
	calls := 0
	require.NoError(t, waitForQueueDrain(context.Background(), func(context.Context) (business.AuditQueueSnapshot, error) {
		calls++
		return business.AuditQueueSnapshot{Depth: 0}, nil
	}))
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, waitForQueueDrain(ctx, func(context.Context) (business.AuditQueueSnapshot, error) {
		return business.AuditQueueSnapshot{Depth: 2}, nil
	}))
}

// Exercise the qualifier through the actual warehouse reader and Storage Read
// fake. Its source selector requires datasource envelopes, unlike an arbitrary
// stand-in that would accept every event with a matching resource identifier.
type readerProbeStore struct{ *bigquerystore.Reader }

func (readerProbeStore) AppendAuditBatch(context.Context, business.AuditBatch) error { return nil }
func TestQualificationAgainstWarehouseReaderAndArrowStreams(t *testing.T) {
	o := probeOptions()
	records, err := qualificationRecords(o)
	require.NoError(t, err)
	fake := bigqueryfake.New()
	fake.CreateTable(bigqueryfake.TablePath("example-project", "audit", bigquerystore.EventsTable), bigquerystore.EventsSchema())
	fake.CreateTable(bigqueryfake.TablePath("example-project", "audit", bigquerystore.DetailsTable), bigquerystore.DetailsSchema())
	for _, deployment := range []string{"deployment-1", "different-deployment"} {
		events, details := bigquerystore.BatchRows(business.AuditBatch{ID: uuid.NewString(), DeploymentID: deployment, ComposedAt: o.from, Records: records})
		for _, row := range events {
			require.NoError(t, fake.Insert(bigqueryfake.TablePath("example-project", "audit", bigquerystore.EventsTable), row.Values))
		}
		for _, row := range details {
			require.NoError(t, fake.Insert(bigqueryfake.TablePath("example-project", "audit", bigquerystore.DetailsTable), row.Values))
		}
	}
	reader, err := bigquerystore.NewReader(bigquerystore.ReadConfig{Client: fake, Project: "example-project", Dataset: "audit", DeploymentID: "deployment-1", Now: func() time.Time { return o.to }})
	require.NoError(t, err)
	receipt := probeReceipt(t, o, records)
	require.NoError(t, verifyQualification(context.Background(), o, &probeDB{exists: true}, readerProbeStore{reader}, reader, records, &receipt))
	require.Equal(t, "passed", receipt.Checks["readable_sources"])
	require.Equal(t, "pending", receipt.Checks["deployment_isolation"], "fake coverage cannot count as live peer-cell evidence")
}
