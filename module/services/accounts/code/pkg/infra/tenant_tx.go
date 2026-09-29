package infra

import (
	"accounts/pkg/auth"
	"accounts/pkg/infra/internal/txbind"
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
)

const (
	// tenantDatabaseRole is the only role a request connection may hold.
	tenantDatabaseRole = "app_tenant"
	// controlPlaneDatabaseRole is the audited cross-tenant role WithControlPlane
	// assumes, on the control-plane pool only.
	controlPlaneDatabaseRole = "app_control_plane"
)

// bindRequestScopeSQL binds both halves of a request scope for the current
// transaction only. Every request transaction binds both, the empty string for
// the half it does not carry, so a session-level binding left on the connection
// is never visible inside it: an org transaction cannot read a previous user's
// sessions or mfa_devices, nor a user transaction a previous tenant's rows. The
// policies compare the settings as text and the SECURITY DEFINER operations
// map an empty setting to NULL through NULLIF, so an empty binding matches
// nothing, as an unset one does.
const bindRequestScopeSQL = "SELECT set_config('app.current_org_id', $1, true), set_config('app.current_user_id', $2, true)"

// bindRequestScope binds orgID and userID, either possibly empty, for tx.
func bindRequestScope(ctx context.Context, tx pgx.Tx, orgID, userID string) error {
	_, err := tx.Exec(ctx, bindRequestScopeSQL, orgID, userID)
	return err
}

// controlPlaneCounters tracks, per call site, the cumulative number of
// WithControlPlane invocations during the api process's lifetime. The
// callsite key is "<file>:<line>" of the caller of WithControlPlane —
// gives operators a greppable manifest of every place that intentionally
// skips RLS. Resets on process restart.
//
// Why this matters: every WithControlPlane is a deliberate skip-RLS act.
// In a healthy system the call sites are a short, audited list
// (workers + platform-admin + a few cross-tenant lookups). If a
// new call site appears in production without code review, that's
// a security finding.
//
// Phase 4 of RLS_PLAN.md called for "bypass-role audit": this is
// the runtime half. The static half is TestControlPlaneCallSites, which
// fails when a call site appears or disappears until its committed list
// (testdata/control_plane_call_sites.txt) is updated in review. Combined,
// they're how operators answer "show me every line that bypasses
// RLS today, and how often each fires."
var controlPlaneCounters sync.Map // map[string]*int64

// ControlPlaneCounters returns a snapshot of (callsite → count) for every
// call site that has invoked WithControlPlane since process start.
// Exposed for ops dashboards / smoke tests / audit tooling.
func ControlPlaneCounters() map[string]int64 {
	out := map[string]int64{}
	controlPlaneCounters.Range(func(k, v any) bool {
		out[k.(string)] = atomic.LoadInt64(v.(*int64))
		return true
	})
	return out
}

// orgTxCount is the cumulative WithOrgTx invocation count. Exposed
// via OrgTxCount() so perf-sensitive paths (e.g. GetOrgEntitlements)
// can pin "single transaction across N reads" via a test that checks
// the delta between calls.
var orgTxCount int64

// OrgTxCount returns the cumulative WithOrgTx invocation count.
func OrgTxCount() int64 { return atomic.LoadInt64(&orgTxCount) }

// recordControlPlane increments the per-callsite counter and emits a wool
// debug event. Called once per WithControlPlane invocation.
func recordControlPlane(ctx context.Context) {
	// Caller skip = 2: recordControlPlane → WithControlPlane → real call site.
	_, file, line, ok := runtime.Caller(2)
	site := "unknown"
	if ok {
		site = trimToProjectPath(file) + ":" + itoa(line)
	}
	v, _ := controlPlaneCounters.LoadOrStore(site, new(int64))
	atomic.AddInt64(v.(*int64), 1)
	// Debug-level: visible in dev, not noisy in prod (Info would be
	// too loud given webhook-dispatcher / reconciliation loops fire
	// many times/min). Aggregate counts via ControlPlaneCounters() are the
	// production-grade signal.
	wool.Get(ctx).In("WithControlPlane").Debug("control-plane role assumed", wool.Field("site", site))
}

func trimToProjectPath(file string) string {
	const marker = "/saas-starter/"
	if i := lastIndex(file, marker); i >= 0 {
		return file[i+len(marker):]
	}
	return file
}

func lastIndex(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

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
// Control plane: code paths that legitimately span tenants — the audit
// exporter's poll loop, platform-admin queries, pre-auth lookups — must NOT use
// WithOrgTx. They use WithControlPlane, which runs on a separate pool whose
// login is the only one that can assume app_control_plane (see RLS_PLAN.md).
//
// IMPORTANT — DO NOT NEST: WithOrgTx (and WithControlPlane) call
// pool.Begin(ctx) unconditionally; they don't check whether `ctx`
// already carries a tx. Nesting will check out a SECOND connection
// from the pool while the outer tx still holds the first, which
// deadlocks under pool pressure. Pattern instead:
//
//   - At the Service layer: open ONE WithOrgTx and call multiple
//     Store methods inside it (Store methods reuse the ctx tx via
//     getQueryExecutor).
//   - If a Store method needs its own atomicity (e.g., postgres_org.go
//     CreateOrganization, postgres_permissions.go CreateRole), use
//     the "if hasTx → reuse; else BeginTxFunc" pattern those files
//     demonstrate. Never call WithOrgTx from inside a Store method.
//
// Re-entrancy IS safe across pool.Begin if you're patient enough,
// but only if pool.MaxConns > <number of nested levels>; better not
// to rely on that.
//
// Status: helper is wired and tested. Defense-in-depth coverage:
// migrations 27, 28, 29, 30, 31, 32, 33 — see AUTHZ.md.
func (s *PostgresStore) WithOrgTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if orgID == "" {
		// A WithOrgTx call with empty orgID is almost certainly a bug
		// — if RLS is enabled, the empty setting would silently
		// reject reads. Surface it loudly.
		return errEmptyOrgID
	}
	// Read-only source projection callbacks must not open a second snapshot
	// when existing authority helpers enter their tenant transaction.
	if snapshot, ok := ctx.Value(sourceReadSnapshotKey{}).(sourceReadSnapshot); ok {
		_, owner, _ := auth.VerifiedDatabaseIdentity(ctx)
		if err := auth.RequireVerifiedDatabaseScope(ctx, orgID, owner); err != nil {
			return err
		}
		// The snapshot bound the org it was opened for — the verified tenant
		// orgID was just checked to name — and cleared the user half.
		return fn(txbind.BindRequest(ctx, snapshot.tx, snapshot.orgID, ""))
	}
	atomic.AddInt64(&orgTxCount, 1)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// The connection is already running as app_tenant: the request login
	// starts there and can reach nothing wider (a tooling store selects it on
	// checkout). We just need to set app.current_org_id for the policy, and
	// clear app.current_user_id so no user binding reaches this transaction.
	//
	// set_config is parameterized via pgx; the value is properly
	// quoted and not a SQL-injection vector even if orgID came from
	// user input (which it shouldn't — orgID is always validated as
	// a UUID at the handler layer first).
	if err := bindRequestScope(ctx, tx, orgID, ""); err != nil {
		return err
	}

	// Every Store method's getQueryExecutor() picks the tx up from the
	// context, which also records that it is a request transaction bound for
	// orgID alone, so a helper joining it knows it runs as app_tenant for that
	// org and no user.
	txCtx := txbind.BindRequest(ctx, tx, orgID, "")
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

// WithUserTx is the user-scoped twin of WithOrgTx: runs `fn` inside
// a transaction that has `app.current_user_id` set, so RLS policies
// on user-scoped tables (notifications, mfa_devices, sessions) filter
// to that user. Empty userID is rejected (loud, fail-closed). Same
// no-nesting rule as WithOrgTx — see the comment on that function.
//
// User-scoped tables aren't tenant-scoped (no org_id), but the same
// fail-closed property applies: an un-wrapped Store call against a
// user-RLS-protected table returns zero rows by default because the
// connection runs as `app_tenant` with no `app.current_user_id` set. Cross-user readers (platform
// admin / refresh-token-hash lookup during login) use WithControlPlane.
func (s *PostgresStore) WithUserTx(ctx context.Context, userID string, fn func(ctx context.Context) error) error {
	if userID == "" {
		return errEmptyUserID
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	// Clear app.current_org_id so no tenant binding reaches this transaction.
	if err := bindRequestScope(ctx, tx, "", userID); err != nil {
		return err
	}
	txCtx := txbind.BindRequest(ctx, tx, "", userID)
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const errEmptyUserID errString = "WithUserTx: userID is required"

// WithControlPlane runs `fn` inside a transaction that assumes the named,
// audited app_control_plane database role for the tx duration. Used ONLY for:
//
//   - Background workers that legitimately scan across tenants
//     (billing reconciliation, scheduled cleanup jobs).
//   - Platform-admin endpoints that present a cross-org view.
//
// Mechanism: the transaction runs on the control-plane pool, whose login is
// distinct from the request login and holds explicit SET membership in
// app_control_plane, and assumes it with `SET LOCAL ROLE app_control_plane`.
// The request login cannot reach that role at all, so nothing on a request
// connection can widen itself into this one. On commit/rollback the local role
// unwinds before the connection returns to the control-plane pool.
//
// Treat it like sudo — every call site should be deliberate, with a
// comment explaining why it can't use WithOrgTx. Invariant: a
// WithControlPlane-wrapped function MUST NOT use user-supplied input as a
// filter without explicit SQL — the policy isn't there to catch
// you.
func (s *PostgresStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	// Record here, rather than inside withControlPlaneTx, so the metric retains
	// the actual capability call site instead of collapsing every caller onto
	// this helper.
	recordControlPlane(ctx)
	return s.withControlPlaneTx(ctx, pgx.TxOptions{}, fn)
}

func (s *PostgresStore) withControlPlaneTx(ctx context.Context, options pgx.TxOptions, fn func(ctx context.Context) error) error {
	tx, err := s.controlPlane.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
		return err
	}

	// Store methods called inside fn run on this transaction through
	// getQueryExecutor; the binding records it as a control-plane one, so
	// Scoped.Within refuses to run a tenant or user identity inside it.
	txCtx := txbind.BindControlPlane(ctx, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
