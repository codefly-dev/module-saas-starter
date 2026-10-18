package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// Migration 41: the inbox holds module manifests only. Rows of the replaced
// document family are removed, live applied state is cleared for the next
// delivered manifest to re-establish on its installation target, a recorded
// withdrawal is kept, host_component goes, and the down refuses while a
// manifest is stored.
func TestMigration41ModuleManifestInboxAndDown(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 40), databaseURL); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("ab", 32)
	for _, kind := range []string{"presence", "authority", "endpoints"} {
		execColdCutoverSQL(t, db, `INSERT INTO solution_delivery_documents(kind,document_id,generation,content_hash,payload,bundle,signer_identity,ownership_domain,received_at)
			VALUES ('`+kind+`','acme.test.live',1,'`+hash+`','{}'::bytea,'{}'::bytea,'example-key','acme',now())`)
	}
	execColdCutoverSQL(t, db, `INSERT INTO solution_host_bindings(binding_id,host_coordinate,host_component,
			desired_generation,desired_digest,desired_document,desired_seen_at,
			applied_generation,applied_digest,applied_document,applied_routes,applied_solution_id,applied_release,applied_domain,applied_at)
		VALUES ('acme.test.live','example-host','example-component',1,'`+hash+`','presence',now(),
			1,'`+hash+`','presence','{live}','live','acme/live@1.0.0','acme',now())`)
	execColdCutoverSQL(t, db, `INSERT INTO solution_host_bindings(binding_id,host_coordinate,host_component,
			applied_generation,applied_digest,applied_document,applied_removed,applied_solution_id,applied_release,applied_domain,applied_at)
		VALUES ('acme.test.gone','example-host','example-component',2,'`+hash+`','presence',true,'gone','acme/gone@1.0.0','acme',now())`)

	ledger := ledgerUpTo(t, 41)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}
	var count string
	mustQuery(t, db, `SELECT count(*)::text FROM solution_delivery_documents`, &count)
	if count != "0" {
		t.Fatalf("the replaced document family must leave the inbox, %s rows remain", count)
	}
	if _, err := db.Exec(`INSERT INTO solution_delivery_documents(kind,document_id,generation,content_hash,payload,bundle,signer_identity,ownership_domain,received_at)
		VALUES ('presence','acme.test.live',2,$1,'{}'::bytea,'{}'::bytea,'example-key','acme',now())`, hash); err == nil {
		t.Fatal("the inbox must refuse a presence row")
	}
	execColdCutoverSQL(t, db, `INSERT INTO solution_delivery_documents(kind,document_id,generation,content_hash,payload,bundle,signer_identity,ownership_domain,received_at)
		VALUES ('manifest','acme.test.live',2,'`+hash+`','{}'::bytea,'{}'::bytea,'example-key','acme',now())`)
	var live, gone string
	mustQuery(t, db, `SELECT (applied_generation IS NULL AND desired_generation IS NULL AND cardinality(applied_routes)=0)::text
		FROM solution_host_bindings WHERE binding_id='acme.test.live'`, &live)
	mustQuery(t, db, `SELECT (applied_removed AND applied_generation = 2 AND applied_document = 'presence')::text
		FROM solution_host_bindings WHERE binding_id='acme.test.gone'`, &gone)
	if live != "true" || gone != "true" {
		t.Fatalf("live applied state must clear (%s) and a withdrawal must stay (%s)", live, gone)
	}
	if _, err := db.Exec(`SELECT host_component FROM solution_host_bindings`); err == nil {
		t.Fatal("host_component must be dropped")
	}
	// A binding keeps the domain it applied under when its live state is
	// cleared, so no other domain can take its id before its owner redelivers.
	var liveDomain, goneDomain string
	mustQuery(t, db, `SELECT first_domain FROM solution_host_bindings WHERE binding_id='acme.test.live'`, &liveDomain)
	mustQuery(t, db, `SELECT first_domain FROM solution_host_bindings WHERE binding_id='acme.test.gone'`, &goneDomain)
	if liveDomain != "acme" || goneDomain != "acme" {
		t.Fatalf("first_domain must keep each binding's applied domain, got %q and %q", liveDomain, goneDomain)
	}
	if _, err := db.Exec(`UPDATE solution_host_bindings SET first_domain = '' WHERE binding_id='acme.test.live'`); err == nil {
		t.Fatal("first_domain must refuse an empty domain")
	}

	abs, err := filepath.Abs(ledger)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.New("file://"+abs, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = runner.Close() }()
	if err := runner.Steps(-1); err == nil || !strings.Contains(err.Error(), "migration 41 cannot be reverted") {
		t.Fatalf("the down must refuse while a manifest is stored, got %v", err)
	}
	if err := runner.Force(41); err != nil {
		t.Fatal(err)
	}
	execColdCutoverSQL(t, db, `DELETE FROM solution_delivery_documents WHERE kind='manifest'`)
	if err := runner.Steps(-1); err != nil {
		t.Fatal(err)
	}
	var component string
	mustQuery(t, db, `SELECT host_component FROM solution_host_bindings WHERE binding_id='acme.test.gone'`, &component)
	if component != "example-host" {
		t.Fatalf("the down restores host_component from the coordinate, got %q", component)
	}
	if _, err := db.Exec(`SELECT first_domain FROM solution_host_bindings`); err == nil {
		t.Fatal("the down must drop first_domain")
	}
}
