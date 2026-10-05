package auditops

import (
	"accounts/pkg/auditstore/auditsink"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"reflect"
	"time"
)

// QualificationReceipt contains only fixture identifiers, hashes and checks
// actually observed. Gateway, archive, alert and controlled outage acceptance
// need separate observers and remain pending here.
type QualificationReceipt struct {
	Schema        string               `json:"schema"`
	DeploymentID  string               `json:"deployment_id"`
	Sink          string               `json:"sink"`
	Configuration map[string]string    `json:"configuration"`
	RunID         string               `json:"run_id"`
	ObservedAt    time.Time            `json:"observed_at"`
	WindowFrom    time.Time            `json:"window_from"`
	WindowTo      time.Time            `json:"window_to"`
	Events        []QualificationEvent `json:"events"`
	Checks        map[string]string    `json:"checks"`
	ErrorCode     string               `json:"error_code"`
}
type QualificationEvent struct {
	ID            string                       `json:"id"`
	OrgID         string                       `json:"org_id"`
	Retention     business.AuditRetentionClass `json:"retention"`
	DetailsSHA256 string                       `json:"details_sha256"`
}
type organizations []string

func (o *organizations) String() string { return "" }
func (o *organizations) Set(value string) error {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return errors.New("organization must be a nonzero UUID")
	}
	*o = append(*o, id.String())
	return nil
}

type qualificationOptions struct {
	orgs     organizations
	runID    uuid.UUID
	prepare  bool
	from, to time.Time
	timeout  time.Duration
}

func parseQualification(args []string, stderr io.Writer, now time.Time) (qualificationOptions, bool, error) {
	var o qualificationOptions
	fs := flag.NewFlagSet("audit-qualify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&o.orgs, "organization", "existing organization UUID; repeat twice")
	run := fs.String("run-id", "", "unique qualification UUID")
	prep := fs.Bool("prepare", false, "write six bounded fixtures through RecordTx")
	verify := fs.Bool("verify", false, "observe fixtures without writing")
	from := fs.String("window-from", "", "prepare receipt window_from")
	to := fs.String("window-to", "", "prepare receipt window_to")
	deadline := fs.Duration("timeout", 2*time.Minute, "polling deadline, at most five minutes")
	_ = fs.Bool("json", false, "emit machine evidence")
	cap := fs.Bool("capabilities-json", false, "report capabilities without connecting")
	if err := fs.Parse(args); err != nil {
		return o, false, errors.New("invalid flags")
	}
	if fs.NArg() != 0 {
		return o, false, errors.New("unexpected arguments")
	}
	if *cap {
		return o, true, nil
	}
	var err error
	o.runID, err = uuid.Parse(*run)
	if err != nil || o.runID == uuid.Nil || len(o.orgs) != 2 || o.orgs[0] == o.orgs[1] || *prep == *verify {
		return o, false, errors.New("unique run UUID, two distinct organization UUIDs and exactly one phase required")
	}
	if *deadline <= 0 || *deadline > 5*time.Minute {
		return o, false, errors.New("timeout must be positive and at most five minutes")
	}
	o.prepare, o.timeout = *prep, *deadline
	if *prep {
		if *from != "" || *to != "" {
			return o, false, errors.New("prepare chooses its own window")
		}
		o.from = now.UTC().Truncate(time.Microsecond)
		o.to = o.from.Add(5 * time.Minute)
	} else {
		o.from, err = time.Parse(time.RFC3339Nano, *from)
		if err != nil {
			return o, false, errors.New("receipt window-from required")
		}
		o.to, err = time.Parse(time.RFC3339Nano, *to)
		if err != nil {
			return o, false, errors.New("receipt window-to required")
		}
		o.from, o.to = o.from.UTC(), o.to.UTC()
		if !o.to.After(o.from) || o.to.Sub(o.from) > 5*time.Minute || o.from.Before(now.Add(-24*time.Hour)) || o.from.After(now.Add(time.Minute)) {
			return o, false, errors.New("recent receipt window of at most five minutes required")
		}
	}
	return o, false, nil
}
func qualificationRecords(o qualificationOptions) ([]business.AuditRecord, error) {
	var records []business.AuditRecord
	for _, org := range []string{o.orgs[0], o.orgs[1], ""} {
		for _, class := range []business.AuditRetentionClass{business.RetentionSecurity, business.RetentionContent} {
			id := uuid.NewSHA1(o.runID, []byte(org+"/"+string(class))).String()
			typ := business.EventAuthLogin
			payload := map[string]any{"method": "audit-qualification", "probe_run_id": o.runID.String()}
			if class == business.RetentionContent {
				typ = business.EventDatasourceSourceSynced
				payload = map[string]any{"job_id": o.runID.String(), "repo": "example/audit-qualification", "probe_run_id": o.runID.String()}
			}
			e := business.AuditEntry{ID: id, OrgID: org, ActorType: business.ActorTypeSystem, EventType: typ, SchemaVersion: 1, Resource: "audit-qualification", ResourceID: id, CreatedAt: o.from, Payload: payload, IdempotencyKey: "audit-qualification/" + o.runID.String() + "/" + string(class)}
			if class == business.RetentionContent {
				e.Resource = "datasource"
			}
			record, err := business.NewAuditRecord(e, class)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
	}
	return records, nil
}
func newQualificationReceipt(o qualificationOptions, s *auditsink.Swap, records []business.AuditRecord) QualificationReceipt {
	r := QualificationReceipt{Schema: "codefly/audit-qualification/v1", DeploymentID: s.DeploymentID, Sink: string(s.Mode), Configuration: safeConfiguration(s), RunID: o.runID.String(), ObservedAt: time.Now().UTC(), WindowFrom: o.from, WindowTo: o.to, Events: []QualificationEvent{}, Checks: map[string]string{}}
	for _, key := range []string{"transaction_queue", "queue_drained", "warehouse_readback", "scoped_list", "aggregation", "export", "readable_sources", "organization_isolation", "deployment_isolation", "postgres_no_new_rows", "archive_object_readback", "archive_lock_enforcement", "gateway_authorization", "relay_interruption_recovery", "alert_delivery"} {
		r.Checks[key] = "pending"
	}
	for _, v := range records {
		r.Events = append(r.Events, QualificationEvent{v.Entry.ID, v.Entry.OrgID, v.Retention, v.DetailsSHA256})
	}
	return r
}
func RunQualification(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	o, cap, err := parseQualification(args, stderr, time.Now())
	if err != nil {
		line(stderr, "audit-qualify: invalid qualification options")
		return 2
	}
	if cap {
		return writeCapabilities(stdout, getenv)
	}
	cfg, err := auditsink.Load(getenv)
	if err != nil || cfg.Swap == nil {
		line(stderr, "audit-qualify: configured warehouse required")
		return 1
	}
	records, err := qualificationRecords(o)
	if err != nil {
		return 1
	}
	r := newQualificationReceipt(o, cfg.Swap, records)
	fail := func(code string) int {
		r.ObservedAt = time.Now().UTC()
		r.ErrorCode = code
		_ = json.NewEncoder(stdout).Encode(r)
		return 1
	}
	ctx, err := commandContext(context.Background())
	if err != nil {
		return fail("runtime_configuration_failed")
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	db, err := openStore(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return fail("database_unavailable")
	}
	defer db.Close()
	if o.prepare {
		emitter, err := business.NewDurableAuditEmitter(db, db, business.WithQueuedRecords())
		if err != nil {
			return fail("fixture_configuration_failed")
		}
		if err = prepareQualification(ctx, o, db, emitter, records, &r); err != nil {
			return fail("fixture_transaction_failed")
		}
	} else {
		opened, err := auditsink.Open(ctx, cfg.Swap)
		if err != nil {
			return fail("warehouse_unavailable")
		}
		defer opened.Close()
		if err = verifyQualification(ctx, o, db, opened.Store, opened.History, records, &r); err != nil {
			return fail("qualification_failed")
		}
		var depthProbe func(context.Context) (business.AuditQueueSnapshot, error)
		var poolErr error
		var pool *pgxpool.Pool
		if getenv("DATABASE_URL") != "" {
			pool, poolErr = infra.NewAuditRelayPoolFromURL(ctx, getenv("DATABASE_URL"))
		} else {
			pool, poolErr = infra.NewAuditRelayPool(ctx)
		}
		if poolErr != nil {
			return fail("queue_unavailable")
		}
		defer pool.Close()
		queue, queueErr := infra.NewPostgresAuditQueue(pool)
		if queueErr != nil {
			return fail("queue_unavailable")
		}
		depthProbe = queue.Snapshot
		if waitForQueueDrain(ctx, depthProbe) != nil {
			r.Checks["queue_drained"] = "failed"
			return fail("queue_not_drained")
		}
		r.Checks["queue_drained"] = "passed"
	}
	r.ObservedAt = time.Now().UTC()
	if json.NewEncoder(stdout).Encode(r) != nil {
		return 1
	}
	return 0
}

type qualificationDB interface {
	WithControlPlane(context.Context, func(context.Context) error) error
	OrganizationIDExists(context.Context, string) (bool, error)
	CountAuditHistory(context.Context, time.Time, time.Time) (map[string]int64, error)
}

func postgresProbe(ctx context.Context, o qualificationOptions, db qualificationDB) error {
	counts, err := db.CountAuditHistory(ctx, o.from, o.to)
	if err != nil {
		return err
	}
	for _, n := range counts {
		if n != 0 {
			return errors.New("postgres received new audit rows")
		}
	}
	return nil
}
func prepareQualification(ctx context.Context, o qualificationOptions, db qualificationDB, recorder business.AuditRecorder, records []business.AuditRecord, r *QualificationReceipt) error {
	err := db.WithControlPlane(ctx, func(ctx context.Context) error {
		for _, org := range o.orgs {
			exists, err := db.OrganizationIDExists(ctx, org)
			if err != nil || !exists {
				return errors.New("existing organization required")
			}
		}
		for _, v := range records {
			if err := recorder.RecordTx(ctx, v.Entry); err != nil {
				return err
			}
		}
		return postgresProbe(ctx, o, db)
	})
	if err != nil {
		r.Checks["transaction_queue"] = "failed"
		return err
	}
	r.Checks["transaction_queue"], r.Checks["postgres_no_new_rows"] = "passed", "passed"
	return nil
}

var errFixturesPending = errors.New("fixtures not delivered yet")

func warehouseProbe(ctx context.Context, o qualificationOptions, history business.AuditHistoryReader, records []business.AuditRecord, deployment string) error {
	expected := map[string]business.AuditRecord{}
	for _, v := range records {
		expected[v.Entry.ID] = v
	}
	seen := map[string]bool{}
	scanned := 0
	err := history.ReadStoredAuditEvents(ctx, o.from, o.to, func(got business.StoredAuditEvent) error {
		scanned++
		if scanned > 20000 {
			return errors.New("event read bound exceeded")
		}
		want, ok := expected[got.Entry.ID]
		if !ok {
			return nil
		}
		if got.DeploymentID != deployment || got.Retention != want.Retention || got.DetailsSHA256 != want.DetailsSHA256 || !got.HasDetails || business.AuditDetailsSHA256(got.Details) != want.DetailsSHA256 || !sameEnvelope(got.Entry, want.Entry) {
			return errors.New("fixture envelope or details hash differs")
		}
		seen[got.Entry.ID] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errFixturesPending
	}
	return nil
}
func verifyQualification(ctx context.Context, o qualificationOptions, db qualificationDB, store business.AuditStore, history business.AuditHistoryReader, records []business.AuditRecord, r *QualificationReceipt) error {
	for {
		err := warehouseProbe(ctx, o, history, records, r.DeploymentID)
		if err == nil {
			r.Checks["warehouse_readback"], r.Checks["transaction_queue"] = "passed", "passed"
			break
		}
		if !errors.Is(err, errFixturesPending) {
			r.Checks["warehouse_readback"] = "failed"
			return err
		}
		select {
		case <-ctx.Done():
			r.Checks["warehouse_readback"] = "failed"
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if err := db.WithControlPlane(ctx, func(ctx context.Context) error { return postgresProbe(ctx, o, db) }); err != nil {
		r.Checks["postgres_no_new_rows"] = "failed"
		return err
	}
	r.Checks["postgres_no_new_rows"] = "passed"
	for _, key := range []string{"scoped_list", "aggregation", "export", "readable_sources", "organization_isolation"} {
		r.Checks[key] = "passed"
	}
	for _, org := range []string{o.orgs[0], o.orgs[1], ""} {
		scope := business.PlatformAuditScope()
		if org != "" {
			scope = business.OrganizationAuditScope(org)
		}
		read := business.AuditRead{Scope: scope, Query: business.AuditQuery{OrgID: org, From: &o.from, To: &o.to, PayloadContains: map[string]any{"probe_run_id": o.runID.String()}, PageSize: 20}}
		want := map[string]business.AuditRecord{}
		for _, v := range records {
			if org == "" || v.Entry.OrgID == org {
				want[v.Entry.ID] = v
			}
		}
		entries, token, err := store.ListAuditEvents(ctx, read)
		if err != nil || token != "" || !matchEntries(entries, want) {
			r.Checks["scoped_list"], r.Checks["organization_isolation"] = "failed", "failed"
			continue
		}
		exported, err := store.ExportAuditEvents(ctx, read)
		if err != nil || !matchEntries(exported, want) {
			r.Checks["export"] = "failed"
		}
		buckets, err := store.AggregateAuditEvents(ctx, read, business.AuditAggregationSpec{GroupBy: []string{"event_type"}, Metrics: []business.AuditMetric{{Op: "count"}}})
		expectedCounts := map[string]float64{}
		for _, v := range want {
			expectedCounts[string(v.Entry.EventType)]++
		}
		observedCounts := map[string]float64{}
		valid := err == nil
		for _, b := range buckets {
			if len(b.Keys) != 1 || observedCounts[b.Keys[0]] != 0 || b.Metrics["count"] != expectedCounts[b.Keys[0]] {
				valid = false
				continue
			}
			observedCounts[b.Keys[0]] = b.Metrics["count"]
		}
		if !valid || !reflect.DeepEqual(observedCounts, expectedCounts) {
			r.Checks["aggregation"] = "failed"
		}
		if org != "" {
			var source string
			for _, v := range records {
				if v.Entry.OrgID == org && v.Retention == business.RetentionContent {
					source = v.Entry.ResourceID
				}
			}
			allSources := []string{}
			for _, v := range records {
				if v.Retention == business.RetentionContent {
					allSources = append(allSources, v.Entry.ResourceID)
				}
			}
			sources, err := store.LatestSourceSyncEvents(ctx, scope, allSources)
			if err != nil || len(sources) != 1 || !sources[source].RequestedAt.Equal(o.from) || sources[source].ActorID != "" {
				r.Checks["readable_sources"] = "failed"
			}
		}
	}
	_, _, err := store.ListAuditEvents(ctx, business.AuditRead{})
	if err == nil {
		r.Checks["organization_isolation"] = "failed"
	}
	_, _, err = store.ListAuditEvents(ctx, business.AuditRead{Scope: business.OrganizationAuditScope(o.orgs[0]), Query: business.AuditQuery{OrgID: o.orgs[1]}})
	if err == nil {
		r.Checks["organization_isolation"] = "failed"
	}
	for _, v := range r.Checks {
		if v == "failed" {
			return errors.New("read qualification failed")
		}
	}
	return nil
}
func sameEnvelope(got, want business.AuditEntry) bool {
	got.Payload, want.Payload = nil, nil
	got.IdempotencyKey, want.IdempotencyKey = "", ""
	got.CreatedAt, want.CreatedAt = got.CreatedAt.UTC(), want.CreatedAt.UTC()
	return reflect.DeepEqual(got, want)
}
func matchEntries(entries []business.AuditEntry, want map[string]business.AuditRecord) bool {
	if len(entries) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		record, ok := want[entry.ID]
		if !ok || seen[entry.ID] || !sameEnvelope(entry, record.Entry) {
			return false
		}
		details, err := business.CanonicalAuditDetails(entry.Payload)
		if err != nil || business.AuditDetailsSHA256(details) != record.DetailsSHA256 {
			return false
		}
		seen[entry.ID] = true
	}
	return true
}

// waitForQueueDrain observes the whole cell queue on the worker authority;
// delivery of just the fixtures does not establish that the queue is empty.
func waitForQueueDrain(ctx context.Context, observe func(context.Context) (business.AuditQueueSnapshot, error)) error {
	for {
		snapshot, err := observe(ctx)
		if err != nil {
			return err
		}
		if snapshot.Depth == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
