package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// requiredSchemaVersion is the highest store migration this binary was built
// with. A binary serves only a store at this version or later (a store ahead is
// a rollback of accounts, which the ledger's forward-only rule allows). The
// lockstep test fails when a migration is added without moving this number, so
// a binary can never be released that does not know the schema it needs.
const requiredSchemaVersion = 72

// schemaReader is what the check needs; a pool and a transaction both satisfy it.
type schemaReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// controlPlaneSchemaVersion reads the version as the control-plane role, the
// one migration 72 grants it to, rather than as whatever role the login's
// session defaults to.
func controlPlaneSchemaVersion(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("open control-plane Postgres connection: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // the probe changes nothing
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
		return fmt.Errorf("control-plane Postgres login cannot assume %s: %w", controlPlaneDatabaseRole, err)
	}
	return requireSchemaCurrent(ctx, tx)
}

// requireSchemaCurrent refuses a store that is behind this binary or in the
// middle of a migration. Without it a rollout that starts accounts while the
// store is still migrating does not fail: it serves, and every query that
// touches a new column fails on its own with an error that reads like a missing
// column ("column r.owned does not exist") and names no migration.
func requireSchemaCurrent(ctx context.Context, q schemaReader) error {
	var version int64
	var dirty bool
	err := q.QueryRow(ctx, `SELECT version, dirty FROM public.schema_migrations`).Scan(&version, &dirty)
	return schemaRefusal(version, dirty, err)
}

// schemaRefusal is the decision, separated from the query so every shape of
// refusal is a test case rather than a database.
func schemaRefusal(version int64, dirty bool, readErr error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.As(readErr, &pgErr) && pgErr.Code == "42501":
		// Migration 72 grants the read, so a login refused it is a store that
		// has not applied 72, which is a store behind. (A grant revoked by hand
		// reads the same; the remedy, re-running the ledger, is the same too.)
		return fmt.Errorf("the store has not applied migration %d, which lets accounts read the schema version; this binary requires migration %d: run the store migration and start accounts again", requiredSchemaVersion, requiredSchemaVersion)
	case errors.As(readErr, &pgErr) && pgErr.Code == "42P01":
		return fmt.Errorf("the store has no schema version table: it has not been migrated; this binary requires migration %d", requiredSchemaVersion)
	case errors.Is(readErr, pgx.ErrNoRows):
		return fmt.Errorf("the store records no schema version: it has not been migrated; this binary requires migration %d", requiredSchemaVersion)
	case readErr != nil:
		return fmt.Errorf("read the store's schema version: %w", readErr)
	case dirty:
		return fmt.Errorf("the store is at migration %d and marked dirty: a migration is running or failed; this binary requires migration %d. Wait for the migration to finish, or, if it failed, fix and force the version before starting accounts", version, requiredSchemaVersion)
	case version < requiredSchemaVersion:
		return fmt.Errorf("the store is at migration %d and this binary requires migration %d: run the store migration (migrations %d through %d are not applied) and start accounts again", version, requiredSchemaVersion, version+1, requiredSchemaVersion)
	}
	return nil
}
