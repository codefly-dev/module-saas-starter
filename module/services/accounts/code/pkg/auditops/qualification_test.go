package auditops

import (
	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/auditstore/bigquerystore"
	"accounts/pkg/business"
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

var probeNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func probeOptions() qualificationOptions {
	return qualificationOptions{orgs: organizations{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, runID: uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc"), from: probeNow, to: probeNow.Add(5 * time.Minute), timeout: time.Minute}
}

type probeDB struct {
	exists bool
	count  int64
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
func (d *probeDB) CountAuditHistory(context.Context, time.Time, time.Time) (map[string]int64, error) {
	return map[string]int64{"": d.count}, nil
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
	_, swap, code := parse(nil, env(swapEnv), &stderr)
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
