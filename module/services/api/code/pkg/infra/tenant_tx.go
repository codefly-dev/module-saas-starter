package infra

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// WithOrgTx runs `fn` inside a transaction that has
// `app.current_org_id` set to `orgID` for its lifetime. This is the
// connection-level handshake the eventual Postgres RLS policies key
// off — every per-tenant table will get a policy of the form:
//
//	CREATE POLICY foo_tenant ON foo
//	  USING (org_id::text = current_setting('app.current_org_id', true));
//
// The `true` second arg to current_setting() means "missing setting
// returns empty string instead of erroring", so a query path that
// forgets to use WithOrgTx returns ZERO ROWS rather than panicking
// — fail-closed.
//
// The provided context carries the transaction value for downstream
// `getQueryExecutor(ctx)` lookups, so existing Store methods don't
// need to be refactored individually — they'll see the tx through
// the standard ctx-tx pattern.
//
// Use SET LOCAL (not SET) so the variable is auto-scoped to the
// transaction; commit/rollback both clear it. No leak across pool
// reuse.
//
// Bypass: code paths that legitimately span tenants — the audit
// exporter's poll loop, migration runner, platform-admin queries —
// must NOT use WithOrgTx. They run against the pool directly using
// a Postgres role that has BYPASSRLS (configured at deploy time;
// see RLS_PLAN.md).
//
// Status: helper is wired and tested. RLS policies are NOT yet
// enabled on any table — that's a per-table rollout (RLS_PLAN.md).
// Adopting WithOrgTx in advance of the policies costs nothing
// (queries still hit normally) and means the day you flip RLS on,
// the wrapped paths Just Work.
func (s *PostgresStore) WithOrgTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if orgID == "" {
		// A WithOrgTx call with empty orgID is almost certainly a bug
		// — if RLS is enabled, the empty setting would silently
		// reject reads. Surface it loudly.
		return errEmptyOrgID
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// SET LOCAL is parameterized via pgx; the value is properly
	// quoted and not a SQL-injection vector even if orgID came from
	// user input (which it shouldn't — orgID is always validated as
	// a UUID at the handler layer first).
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", orgID); err != nil {
		return err
	}

	// Same context key as RunInTransaction so every existing Store
	// method's getQueryExecutor() picks up the tx without any
	// individual refactor. Switching to a typed key would be cleaner
	// Go style but would orphan ~60 method signatures.
	txCtx := context.WithValue(ctx, "tx", tx) //nolint:staticcheck // intentional shared-key with RunInTransaction
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errEmptyOrgID is returned when WithOrgTx is invoked without an
// org id. Loud failure so a bug surfaces in tests, not silently
// in production behind RLS.
type errString string

func (e errString) Error() string { return string(e) }

const errEmptyOrgID errString = "WithOrgTx: orgID is required"

// pgxAlias keeps the import in use; pgx.Tx is referenced by Pool
// interactions transitively but the linter doesn't see it from this
// file alone.
var _ pgx.Tx = (pgx.Tx)(nil)
