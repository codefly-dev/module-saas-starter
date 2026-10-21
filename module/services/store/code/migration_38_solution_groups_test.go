package main

import (
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestMigration38SolutionGroupPresentationAndDown(t *testing.T) {
	db, databaseURL := throwawayPostgres(t)
	if err := migrateStoreFrom("file://"+ledgerUpTo(t, 37), databaseURL); err != nil {
		t.Fatal(err)
	}
	execColdCutoverSQL(t, db, `INSERT INTO solution_host_bindings(binding_id,host_coordinate,host_component)
		VALUES ('acme.test.grouped','example-host','example-component')`)
	var target string
	mustQuery(t, db, `INSERT INTO solution_targets(binding_id,solution_id,opened_generation)
		VALUES ('acme.test.grouped','example',1) RETURNING id::text`, &target)
	if _, err := db.Exec(`INSERT INTO solution_registrations(solution_id,publisher,revision,
		declared_binding_id,declared_generation,declared_release,declared_target_id,declared_kind)
		VALUES ('example','solution:example',1,'acme.test.grouped',1,'acme/example@1.0.0',$1::uuid,'solution')`, target); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerUpTo(t, 38)
	if err := migrateStoreFrom("file://"+ledger, databaseURL); err != nil {
		t.Fatal(err)
	}
	// A headless solution can be grouped: no frontend column is needed.
	execColdCutoverSQL(t, db, `UPDATE solution_registrations SET solution_group='{"id":"operations","title":"Operations","order":1}' WHERE solution_id='example'`)
	var id, sameTarget string
	if err := db.QueryRow(`SELECT solution_group->>'id',declared_target_id::text FROM solution_registrations WHERE solution_id='example'`).Scan(&id, &sameTarget); err != nil {
		t.Fatal(err)
	}
	if id != "operations" || sameTarget != target {
		t.Fatal("group did not round trip with unchanged installation target")
	}
	for _, query := range []string{
		`UPDATE solution_registrations SET declared_kind='module' WHERE solution_id='example'`,
		`UPDATE solution_registrations SET tombstoned_at=now() WHERE solution_id='example'`,
		`UPDATE solution_registrations SET solution_group='[]' WHERE solution_id='example'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("invalid group accepted: %s", query)
		}
	}
	abs, err := filepath.Abs(ledger)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.New("file://"+abs, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT declared_target_id::text FROM solution_registrations WHERE solution_id='example'`).Scan(&sameTarget); err != nil {
		t.Fatal(err)
	}
	if sameTarget != target {
		t.Fatal("downgrade changed installation identity")
	}
}
