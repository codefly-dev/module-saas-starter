package auditmetricstest

import (
	"context"
	"os"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type readerStore struct {
	business.Store
	allowed bool
	called  bool
	org     string
	query   business.AuditQuery
}

func (s *readerStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	s.org = org
	return fn(ctx)
}
func (s *readerStore) CheckAccess(_ context.Context, reader string, kind gen.SubjectKind, resource, id, action string) (bool, string, error) {
	return s.allowed && reader == "reader" && kind == gen.SubjectKind_SUBJECT_KIND_PRINCIPAL && s.org == "org-a" && resource == "collection" && id == "source-a" && action == "read", "", nil
}
func (s *readerStore) AggregateAuditLog(_ context.Context, q business.AuditQuery, _ business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	s.called = true
	s.query = q
	return nil, nil
}
func TestResourceReadRechecked(t *testing.T) {
	store := &readerStore{allowed: true}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	q := business.AuditQuery{OrgID: "org-a", Resource: "collection", ResourceID: "source-a", EventType: "saas.document.ingested", PayloadContains: map[string]any{"run_id": "run-a"}}
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.NoError(t, err)
	require.Equal(t, q, store.query)
	for _, variant := range []string{"revoked", "other-org", "other-resource", "other-reader"} {
		t.Run(variant, func(t *testing.T) {
			store.allowed = true
			store.called = false
			other := q
			reader := "reader"
			switch variant {
			case "revoked":
				store.allowed = false
			case "other-org":
				other.OrgID = "org-b"
			case "other-resource":
				other.ResourceID = "source-b"
			case "other-reader":
				reader = "other"
			}
			_, err = svc.AggregateAuditLogForReader(context.Background(), reader, other, business.AuditAggregationSpec{})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.False(t, store.called)
		})
	}
	q.OrgID = ""
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// The DSN must point to an independent disposable database. No service harness,
// shared ports, credentials or installed application schema are used.
func TestPostgresIsolationDedupeAndUnknownTelemetry(t *testing.T) {
	dsn := os.Getenv("AUDIT_METRICS_TEST_DSN")
	if dsn == "" {
		t.Skip("set AUDIT_METRICS_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE audit_events (org_id text, resource text, resource_id text, event_type text, actor_id text, created_at timestamptz, payload jsonb);
 INSERT INTO audit_events VALUES
 ('org-a','collection','source-a','saas.document.ingested','reader','2026-01-02','{"run_id":"run-a","logical_job_id":"job-a","documents":2}'),
 ('org-a','collection','source-a','saas.document.ingested','reader','2026-01-02','{"run_id":"run-a","logical_job_id":"job-a"}'),
 ('org-b','collection','source-a','saas.document.ingested','reader','2026-01-02','{"run_id":"run-a","logical_job_id":"job-b","documents":90}'),
 ('org-a','collection','source-b','saas.document.ingested','reader','2026-01-02','{"run_id":"run-a","logical_job_id":"job-c","documents":90}'),
 ('org-a','collection','source-a','saas.document.ingested','reader','2025-01-02','{"run_id":"run-a","logical_job_id":"job-d","documents":90}'),
 ('org-a','collection','source-a','saas.datasource.sync.completed','reader','2026-01-02','{"run_id":"run-a","logical_job_id":"job-e","documents":90}'),
 ('org-a','collection','source-a','saas.document.ingested','reader','2026-01-02','{"run_id":"run-b","logical_job_id":"job-f","documents":90}');`)
	require.NoError(t, err)
	// Production store explicitly reuses this transaction.
	ctx = context.WithValue(ctx, "tx", tx) //nolint:staticcheck // production transaction key
	store := &infra.PostgresStore{}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(48 * time.Hour)
	q := business.AuditQuery{OrgID: "org-a", Resource: "collection", ResourceID: "source-a", EventType: "saas.document.ingested", PayloadContains: map[string]any{"run_id": "run-a"}, From: &from, To: &to}
	spec := business.AuditAggregationSpec{Metrics: []business.AuditMetric{{Op: "count_distinct", Field: "payload:logical_job_id", Alias: "jobs"}, {Op: "sum", Field: "payload:documents", Alias: "documents"}, {Op: "sum", Field: "payload:usage", Alias: "usage"}, {Op: "sum", Field: "payload:zero", Alias: "zero"}}, Derived: []business.AuditDerivedMetric{{Alias: "rate", Numerator: "documents", Denominator: "usage"}}}
	rows, err := store.AggregateAuditLog(ctx, q, spec)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	b := rows[0]
	require.EqualValues(t, 2, b.Count)
	require.Equal(t, 1.0, b.Metrics["jobs"])
	require.Equal(t, 2.0, b.Metrics["documents"])
	require.EqualValues(t, 1, b.Samples["documents"])
	require.EqualValues(t, 2, b.Samples["jobs"])
	require.NotContains(t, b.Metrics, "usage")
	require.NotContains(t, b.Metrics, "rate")
	_, err = tx.Exec(ctx, `UPDATE audit_events SET payload = payload || '{"zero":0}'::jsonb`)
	require.NoError(t, err)
	spec.Derived[0].Denominator = "zero"
	rows, err = store.AggregateAuditLog(ctx, q, spec)
	require.NoError(t, err)
	require.Equal(t, 0.0, rows[0].Metrics["zero"])
	require.NotContains(t, rows[0].Metrics, "rate")
	q.PayloadContains = map[string]any{"run_id": "missing' OR true --"}
	rows, err = store.AggregateAuditLog(ctx, q, spec)
	require.NoError(t, err)
	require.Empty(t, rows)
}

type datasourceStore struct {
	readerStore
	boundary string
}

func (s *datasourceStore) GetDatasourceSource(_ context.Context, org, id string) (*business.DatasourceSource, error) {
	if org != "org-a" || id != "source-a" {
		return nil, nil
	}
	return &business.DatasourceSource{ID: id, OrgID: org, BoundaryNodeID: "boundary-a"}, nil
}
func (s *datasourceStore) ListAccessibleScopes(_ context.Context, org, reader string, kind gen.SubjectKind, resource, action, cursor string, limit int) ([]*gen.AccessibleScope, error) {
	if reader != "reader" || resource != "documents" || action != "read" || org != "org-a" {
		return nil, nil
	}
	return []*gen.AccessibleScope{{NodeId: s.boundary}}, nil
}
func TestConnectedSourceUsesCurrentCollectionGrant(t *testing.T) {
	store := &datasourceStore{boundary: "boundary-a"}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	q := business.AuditQuery{OrgID: "org-a", Resource: "datasource", ResourceID: "source-a", EventType: "saas.datasource.sync.completed"}
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.NoError(t, err)
	require.True(t, store.called)
	for _, boundary := range []string{"", "boundary-b"} {
		store.boundary = boundary
		store.called = false
		_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.False(t, store.called)
	}
}

func TestDocumentCollectionFilterAndRevocation(t *testing.T) {
	store := &datasourceStore{boundary: "boundary-a"}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	q := business.AuditQuery{OrgID: "org-a", CollectionID: "boundary-a", EventType: "saas.document.ingested", PayloadContains: map[string]any{"version": "v1"}}
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"version": "v1", "boundary": "boundary-a"}, store.query.PayloadContains)
	require.NotContains(t, q.PayloadContains, "boundary")
	store.boundary = "boundary-b"
	store.called = false
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, store.called)
	store.boundary = "boundary-a"
	q.PayloadContains["boundary"] = "boundary-b"
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	q.PayloadContains = nil
	q.EventType = "saas.datasource.sync.completed"
	_, err = svc.AggregateAuditLogForReader(context.Background(), "reader", q, business.AuditAggregationSpec{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Bind production store methods to the independently owned fixture transaction.
type transactionStore struct {
	*infra.PostgresStore
	tx pgx.Tx
}

func (s *transactionStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	_, err := s.tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", org)
	if err != nil {
		return err
	}
	return fn(context.WithValue(ctx, "tx", s.tx)) //nolint:staticcheck // production transaction key
}
func TestPostgresExistingAndNewSourceBindingRevocation(t *testing.T) {
	dsn := os.Getenv("AUDIT_METRICS_TEST_DSN")
	if dsn == "" {
		t.Skip("set AUDIT_METRICS_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS ltree;
 CREATE TEMP TABLE scope_nodes(id text,org_id text,scope_path ltree,kind text,label text,resource_type text,resource_id text);
 CREATE TEMP TABLE scope_grants(org_id text,subject_kind text,subject_id text,role_id text,scope_path ltree,expires_at timestamptz);
 CREATE TEMP TABLE role_permissions(role_id text,resource text,action text);
 CREATE TEMP TABLE record_shares(org_id text,subject_kind text,subject_id text,role_id text,resource_type text,resource_id text,expires_at timestamptz);
 CREATE TEMP TABLE team_members(team_id text,user_id text);
 CREATE TEMP TABLE datasource_sources(id text,org_id text,provider text DEFAULT 'github',repo text,paths text[] DEFAULT '{}',branch text,boundary_node_id text,credential_secret_ref text DEFAULT '',webhook_secret_ref text,status text DEFAULT 'connected',status_reason text,last_synced_at timestamptz,created_at timestamptz DEFAULT now(),updated_at timestamptz DEFAULT now(),config jsonb,last_ingested_commit text,last_ingested_at timestamptz,last_delivery_id text,reconcile_interval interval DEFAULT '30 minutes',next_reconcile_at timestamptz);
 CREATE TEMP TABLE audit_events(org_id text,resource text,resource_id text,event_type text,actor_id text,created_at timestamptz,payload jsonb);
 INSERT INTO scope_nodes VALUES ('boundary-a','org-a','a','collection','Example collection',NULL,NULL),('boundary-b','org-a','b','collection','Other collection',NULL,NULL),('boundary-other-org','org-b','a','collection','Other org',NULL,NULL);
 INSERT INTO role_permissions VALUES ('reader-role','documents','read');
 INSERT INTO scope_grants VALUES ('org-a','principal','reader','reader-role','a',NULL);
 INSERT INTO datasource_sources(id,org_id,boundary_node_id) VALUES ('existing','org-a','boundary-a');
 INSERT INTO audit_events VALUES ('org-a','datasource','existing','saas.datasource.sync.completed','reader',now(),'{"job_id":"job-a"}');`)
	require.NoError(t, err)
	svc, err := business.NewService(&transactionStore{PostgresStore: &infra.PostgresStore{}, tx: tx})
	require.NoError(t, err)
	q := business.AuditQuery{OrgID: "org-a", Resource: "datasource", ResourceID: "existing", EventType: "saas.datasource.sync.completed"}
	check := func(want codes.Code) {
		t.Helper()
		_, err = svc.AggregateAuditLogForReader(ctx, "reader", q, business.AuditAggregationSpec{})
		require.Equal(t, want, status.Code(err))
	}
	check(codes.OK)
	_, err = tx.Exec(ctx, `INSERT INTO datasource_sources(id,org_id,boundary_node_id) VALUES ('new','org-a','boundary-a')`)
	require.NoError(t, err)
	q.ResourceID = "new"
	check(codes.OK)
	_, err = tx.Exec(ctx, `UPDATE datasource_sources SET boundary_node_id='boundary-b' WHERE id='existing'`)
	require.NoError(t, err)
	q.ResourceID = "existing"
	check(codes.PermissionDenied)
	q.ResourceID = "new"
	check(codes.OK)
	_, err = tx.Exec(ctx, `DELETE FROM scope_grants`)
	require.NoError(t, err)
	check(codes.PermissionDenied)
	_, err = tx.Exec(ctx, `INSERT INTO scope_grants VALUES ('org-b','principal','reader','reader-role','a',NULL)`)
	require.NoError(t, err)
	check(codes.PermissionDenied)
}

func TestReadEventPayloadContract(t *testing.T) {
	for _, event := range []business.EventType{business.EventDocumentRead, business.EventDocumentSearch} {
		payload := map[string]any{"solution": "documents", "boundary": "boundary-a", "correlation_id": "request-a", "outcome": "returned", "result_count": float64(2), "duration_ms": float64(10)}
		require.NoError(t, business.ValidatePayload(event, payload))
		payload["query"] = "must not be accepted"
		require.Error(t, business.ValidatePayload(event, payload))
		delete(payload, "query")
		payload["outcome"] = "answered"
		require.Error(t, business.ValidatePayload(event, payload))
	}
}
