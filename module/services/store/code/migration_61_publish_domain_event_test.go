package main

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// publishDomainEventFacts is everything about the function that a migration can
// change: its body, who owns it, whether it runs with its owner's authority, the
// settings pinned on it and who may execute it.
type publishDomainEventFacts struct {
	body, owner, definer, config, acl string
}

func readPublishDomainEvent(t *testing.T, db *sql.DB) publishDomainEventFacts {
	t.Helper()
	var f publishDomainEventFacts
	err := db.QueryRow(`SELECT prosrc, pg_get_userbyid(proowner), prosecdef::text,
			coalesce(proconfig::text, ''), coalesce(proacl::text, '')
		FROM pg_proc
		WHERE oid = 'public.publish_domain_event(uuid,text,text,text,timestamp with time zone,text,text,text,bytea,uuid,text,text,text,text,text,text,text,integer,bytea)'::regprocedure`).
		Scan(&f.body, &f.owner, &f.definer, &f.config, &f.acl)
	if err != nil {
		t.Fatalf("read publish_domain_event: %v", err)
	}
	return f
}

// Migration 61 replaces publish_domain_event to admit one tenant-less event from
// a tenant-scoped caller. The function's owner, authority, pinned search_path and
// grants must come through the replacement untouched, the owner must be left with
// no CREATE on the schema it borrowed for it, and the down migration must put
// back the body the function had before, byte for byte. Install the ledger below
// 61, take the function's facts, apply 61, step back, and compare; then apply 61
// again, because a down that leaves the ledger unable to go forward is no down.
//
// The canonical migration replay gate supplies the disposable PostgreSQL 16
// cluster through the existing throwawayPostgres helper.
func TestMigration61PublishDomainEventDownRestoresTheBody(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)

	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 60), databaseURL); err != nil {
		t.Fatalf("apply the ledger below migration 61: %v", err)
	}
	before := readPublishDomainEvent(t, db)

	assertOwnerHoldsNoCreate := func(stage string) {
		t.Helper()
		var holds bool
		if err := db.QueryRow(`SELECT has_schema_privilege('app_control_plane', 'public', 'CREATE')`).Scan(&holds); err != nil {
			t.Fatal(err)
		}
		if holds {
			t.Fatalf("%s: app_control_plane still holds CREATE on schema public", stage)
		}
	}
	assertOwnerHoldsNoCreate("below migration 61")

	withUp := "file://" + ledgerUpTo(t, 61)
	if err := migrateStoreFrom(withUp, databaseURL); err != nil {
		t.Fatalf("apply migration 61: %v", err)
	}
	after := readPublishDomainEvent(t, db)
	if after.body == before.body {
		t.Fatal("migration 61 left the function body unchanged")
	}
	if !strings.Contains(after.body, "saas.audit.recorded.v1") {
		t.Fatal("migration 61 does not name the audit journal type in the function body")
	}
	if after.owner != before.owner || after.definer != before.definer || after.config != before.config || after.acl != before.acl {
		t.Fatalf("migration 61 changed more than the body:\n before %+v\n after  %+v", before, after)
	}
	if after.owner != "app_control_plane" || after.definer != "true" ||
		after.config != `{"search_path=pg_catalog, public, pg_temp"}` {
		t.Fatalf("the replacement lost the function's owner, authority or search_path: %+v", after)
	}
	assertOwnerHoldsNoCreate("after migration 61")

	runner, err := migrate.New(withUp, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	stepErr := runner.Steps(-1)
	sourceErr, databaseErr := runner.Close()
	if stepErr != nil || sourceErr != nil || databaseErr != nil {
		t.Fatalf("revert migration 61: step %v, close source %v, close database %v", stepErr, sourceErr, databaseErr)
	}
	if reverted := readPublishDomainEvent(t, db); reverted != before {
		t.Fatalf("the down migration did not restore the function:\n got %+v\nwant %+v", reverted, before)
	}
	assertOwnerHoldsNoCreate("after the down migration")

	if err := migrateStoreFrom(withUp, databaseURL); err != nil {
		t.Fatalf("apply migration 61 again after its down: %v", err)
	}
	if again := readPublishDomainEvent(t, db); again != after {
		t.Fatal("migration 61 applied a second time produced a different body")
	}
	t.Log("migration 61 replaced publish_domain_event keeping owner, definer authority, search_path and grants; its down restored the previous body exactly")
}
