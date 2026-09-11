package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Migration 130 carries solution registrations written before publisher-bound
// registration (#540) over to the verified publisher. The pre-contract gateway
// stored the bare solution id as the publisher; the verified subject is
// `solution:<id>`, and a write naming a different publisher is refused — so
// without the rewrite a deployed registration is locked out by its own
// credential (#613). What no later state reveals is which rows it must leave
// alone: a publisher the caller asserted as something else was never
// authenticated, and a migration does not get to decide who owns it.
//
// This replays the real migration files against rows a deployed database can
// hold.
func TestMigration130CarriesPreContractPublisherOver(t *testing.T) {
	up := migrationSQL(t, "130_solution_registrations_verified_publisher.up.sql")
	down := migrationSQL(t, "130_solution_registrations_verified_publisher.down.sql")

	const (
		preContract = "migration-130-pre-contract"
		asserted    = "migration-130-asserted"
		verified    = "migration-130-verified"
		claimed     = "migration-130-other-publisher"
	)
	seeded := []string{preContract, asserted, verified}

	restored := true
	t.Cleanup(func() {
		asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
			if !restored {
				// A failure between down and up would leave every registration in
				// the shared schema on the pre-contract publisher.
				mustExec(t, ctx, conn, up)
			}
			mustExec(t, ctx, conn,
				`DELETE FROM solution_registrations WHERE solution_id = ANY($1)`, seeded)
		})
	})

	publishers := func() map[string]string {
		t.Helper()
		found := map[string]string{}
		controlPlaneTx(t, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx,
				`SELECT solution_id, publisher FROM solution_registrations WHERE solution_id = ANY($1)`,
				seeded)
			require.NoError(t, err)
			defer rows.Close()
			for rows.Next() {
				var id, publisher string
				require.NoError(t, rows.Scan(&id, &publisher))
				found[id] = publisher
			}
			return rows.Err()
		})
		return found
	}

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, `
			INSERT INTO solution_registrations (solution_id, publisher, revision)
			VALUES ($1, $1, nextval('solution_registry_revision_sequence')),
			       ($2, $4, nextval('solution_registry_revision_sequence')),
			       ($3, 'solution:' || $3, nextval('solution_registry_revision_sequence'))`,
			preContract, asserted, verified, claimed)

		mustExec(t, ctx, conn, up)
	})

	require.Equal(t, map[string]string{
		preContract: "solution:" + preContract,
		asserted:    claimed,
		verified:    "solution:" + verified,
	}, publishers(), "only the pre-contract default is carried over to the verified publisher")

	// Rolling back restores the publisher a pre-#540 gateway would have stored,
	// which for a registration written after the upgrade is also the bare id.
	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		restored = false
		mustExec(t, ctx, conn, down)
	})

	require.Equal(t, map[string]string{
		preContract: preContract,
		asserted:    claimed,
		verified:    verified,
	}, publishers(), "the rollback restores the pre-contract default and still leaves an asserted publisher alone")

	asMigrationOwner(t, func(ctx context.Context, conn *pgxpool.Conn) {
		mustExec(t, ctx, conn, up)
		restored = true
	})

	require.Equal(t, "solution:"+verified, publishers()[verified],
		"down then up must leave a post-upgrade registration on its verified publisher")
}
