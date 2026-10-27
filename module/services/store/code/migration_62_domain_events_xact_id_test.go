package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// publishAsControlPlane writes one journal row through the real
// publish_domain_event, as the role that publishes on behalf of a tenant, and
// returns the id of the transaction that wrote it as the database reports it.
// eventID must be a UUID.
func publishAsControlPlane(t *testing.T, db *sql.DB, eventID, eventType string) string {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SET LOCAL ROLE app_control_plane`); err != nil {
		t.Fatalf("assume app_control_plane: %v", err)
	}
	var xact string
	if err := tx.QueryRow(`SELECT pg_current_xact_id()::text`).Scan(&xact); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT * FROM public.publish_domain_event(
			$1::uuid, $2, 'saas/test', '', now(), '1.0', 'application/json', '', '{}'::bytea,
			NULL, '', '', '', '', '', '', '', 1, decode(repeat('ab', 32), 'hex'))`,
		eventID, eventType); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return xact
}

// Migration 62 adds domain_events.xact_id, the writing transaction's id, as a
// column default, so the journal read can serve only rows whose transaction has
// finished. It must change nothing about publish_domain_event, leave the rows
// that existed NULL and still readable in seq order under the key the read uses,
// stamp every new row with its real transaction id, and revert cleanly. Install
// the ledger below 62, write rows, apply 62, write more, step back, and forward
// again.
//
// The canonical migration replay gate supplies the disposable PostgreSQL 16
// cluster through the existing throwawayPostgres helper.
func TestMigration62DomainEventsXactID(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)

	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 61), databaseURL); err != nil {
		t.Fatalf("apply the ledger below migration 62: %v", err)
	}
	var hasColumn bool
	hasXactColumn := func() bool {
		t.Helper()
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='domain_events' AND column_name='xact_id')`).Scan(&hasColumn); err != nil {
			t.Fatal(err)
		}
		return hasColumn
	}
	if hasXactColumn() {
		t.Fatal("xact_id exists below migration 62")
	}
	function := readPublishDomainEvent(t, db)

	// Rows that exist before the migration: committed, with no transaction id
	// recorded for them.
	const legacyA, legacyB = "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	publishAsControlPlane(t, db, legacyA, "saas.test.legacy")
	publishAsControlPlane(t, db, legacyB, "saas.test.legacy")

	withUp := "file://" + ledgerUpTo(t, 62)
	if err := migrateStoreFrom(withUp, databaseURL); err != nil {
		t.Fatalf("apply migration 62: %v", err)
	}
	if !hasXactColumn() {
		t.Fatal("migration 62 added no xact_id column")
	}
	var dataType, columnDefault string
	if err := db.QueryRow(`SELECT format_type(a.atttypid, a.atttypmod), coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = 'public.domain_events'::regclass AND a.attname = 'xact_id' AND NOT a.attisdropped`).
		Scan(&dataType, &columnDefault); err != nil {
		t.Fatal(err)
	}
	if dataType != "xid8" || columnDefault != "pg_current_xact_id()" {
		t.Fatalf("xact_id is %s default %q, want xid8 default pg_current_xact_id()", dataType, columnDefault)
	}

	// The function is not touched: same body, owner, authority, settings, grants.
	if after := readPublishDomainEvent(t, db); after != function {
		t.Fatalf("migration 62 changed publish_domain_event:\n got %+v\nwant %+v", after, function)
	}

	// Legacy rows stay NULL and are not rewritten into a real transaction id.
	var legacyNull int
	if err := db.QueryRow(`SELECT count(*) FROM public.domain_events WHERE type = 'saas.test.legacy' AND xact_id IS NULL`).Scan(&legacyNull); err != nil {
		t.Fatal(err)
	}
	if legacyNull != 2 {
		t.Fatalf("%d of the 2 rows that existed are NULL, want both", legacyNull)
	}

	// New rows carry the transaction that wrote them, through the unchanged
	// function.
	const newA, newB = "00000000-0000-4000-8000-000000000003", "00000000-0000-4000-8000-000000000004"
	xactA := publishAsControlPlane(t, db, newA, "saas.test.new")
	xactB := publishAsControlPlane(t, db, newB, "saas.test.new")
	for id, want := range map[string]string{newA: xactA, newB: xactB} {
		var got sql.NullString
		if err := db.QueryRow(`SELECT xact_id::text FROM public.domain_events WHERE id = $1::uuid`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !got.Valid || got.String != want {
			t.Fatalf("row %s has xact_id %v, want the writing transaction %s", id, got, want)
		}
	}

	// The read's key puts the legacy block first, in seq order, then the new rows
	// in transaction order, and the index is on exactly that key.
	var order []string
	rows, err := db.Query(`SELECT id::text FROM public.domain_events
		ORDER BY COALESCE(xact_id, '0'::xid8), seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if got, want := strings.Join(order, ","), strings.Join([]string{legacyA, legacyB, newA, newB}, ","); got != want {
		t.Fatalf("read order\n got %s\nwant %s", got, want)
	}
	var indexDef string
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND indexname='idx_domain_events_xact_seq'`).Scan(&indexDef); err != nil {
		t.Fatalf("migration 62 did not create idx_domain_events_xact_seq: %v", err)
	}
	if !strings.Contains(indexDef, "COALESCE(xact_id, '0'::xid8)") || !strings.Contains(indexDef, "seq") {
		t.Fatalf("index is not on the read's key: %s", indexDef)
	}

	runner, err := migrate.New(withUp, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	stepErr := runner.Steps(-1)
	sourceErr, databaseErr := runner.Close()
	if stepErr != nil || sourceErr != nil || databaseErr != nil {
		t.Fatalf("revert migration 62: step %v, close source %v, close database %v", stepErr, sourceErr, databaseErr)
	}
	if hasXactColumn() {
		t.Fatal("the down migration left xact_id behind")
	}
	var leftIndex int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname='idx_domain_events_xact_seq'`).Scan(&leftIndex); err != nil {
		t.Fatal(err)
	}
	if leftIndex != 0 {
		t.Fatal("the down migration left the index behind")
	}
	var kept int
	if err := db.QueryRow(`SELECT count(*) FROM public.domain_events`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 4 {
		t.Fatalf("the down migration left %d rows, want all 4", kept)
	}
	if reverted := readPublishDomainEvent(t, db); reverted != function {
		t.Fatalf("the down migration changed publish_domain_event:\n got %+v\nwant %+v", reverted, function)
	}
	// The function still publishes without the column.
	publishAsControlPlane(t, db, "00000000-0000-4000-8000-000000000005", "saas.test.after_down")

	if err := migrateStoreFrom(withUp, databaseURL); err != nil {
		t.Fatalf("apply migration 62 again after its down: %v", err)
	}
	// Every row that exists when the column comes back is NULL again, the one
	// published while it was gone included, and still reads in seq order.
	var nullAgain int
	if err := db.QueryRow(`SELECT count(*) FROM public.domain_events WHERE xact_id IS NULL`).Scan(&nullAgain); err != nil {
		t.Fatal(err)
	}
	if nullAgain != 5 {
		t.Fatalf("%d rows are NULL after re-applying, want 5", nullAgain)
	}
	t.Log("migration 62 added xact_id as a default without touching publish_domain_event; existing rows stayed NULL and ordered by seq; its down removed the column and index and kept every row")
}
