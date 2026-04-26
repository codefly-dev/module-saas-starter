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

	// The connection is already running as app_tenant via the
	// pool's BeforeAcquire hook (see NewPostgresStoreFromURL).
	// We just need to set app.current_org_id for the policy.
	//
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

// WithBypass runs `fn` inside a transaction that elevates back to
// the connection's session_user (the codefly-managed superuser),
// bypassing RLS for the tx duration. Used ONLY for:
//
//   - Background workers that legitimately scan across tenants
//     (audit-exporter polling, webhook dispatcher, billing
//     reconciliation, scheduled cleanup jobs).
//   - Platform-admin endpoints that present a cross-org view.
//
// Mechanism: the pool's BeforeAcquire hook stamps every checked-out
// connection with `SET ROLE app_tenant`. Inside this tx we run
// `SET LOCAL ROLE NONE`, which reverts current_user to session_user
// for the tx duration. On commit/rollback the SET LOCAL unwinds and
// the connection resumes as app_tenant for any subsequent caller.
//
// We also set `app.bypass = '1'` for policies that key off it
// (defense in depth; today the role switch alone is enough since
// session_user is a superuser, but a future codefly Postgres plugin
// that drops superuser would still be safe via the GUC).
//
// Treat it like sudo — every call site should be deliberate, with a
// comment explaining why it can't use WithOrgTx. Invariant: a
// WithBypass-wrapped function MUST NOT use user-supplied input as a
// filter without explicit SQL — the policy isn't there to catch
// you.
func (s *PostgresStore) WithBypass(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Elevate from app_tenant back to the connection's session_user
	// (the codefly-provided superuser) for the duration of this tx.
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE NONE"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass', '1', true)"); err != nil {
		return err
	}

	txCtx := context.WithValue(ctx, "tx", tx) //nolint:staticcheck // intentional shared-key
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
