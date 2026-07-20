package infra

import (
	"context"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// Scoped is a Store handle bound to a principal Identity. Every call runs inside
// a single transaction pre-seeded with that identity's RLS context —
// app.current_user_id and/or app.current_org_id — or, for business.System(), the
// explicit app_control_plane role. This is how RLS context gets
// established: once, from the identity, instead of each call site reaching for
// WithUserTx / WithOrgTx / WithControlPlane.
//
// Only tenant/user-scoped reads and writes belong on Scoped. Truly-global
// lookups (JWKS, version, cross-tenant scans like GetAPIKeyByHash) stay on the
// base PostgresStore and span tenants explicitly via As(business.System()).
type Scoped struct {
	store *PostgresStore
	id    business.Identity
}

// As returns a Store handle that acts as the given identity. Returns the
// business.Scoped interface (which *Scoped implements) so the business layer can
// call it through the Store interface without an import cycle.
func (s *PostgresStore) As(id business.Identity) business.Scoped {
	return &Scoped{store: s, id: id}
}

// Identity is the principal this handle acts as.
func (sc *Scoped) Identity() business.Identity { return sc.id }

// Within runs fn in one transaction carrying this identity's RLS context. Domain
// methods on Scoped (added per slice in the migration) delegate to it so they
// inherit the identity's row visibility automatically.
//
// One tx sets both vars directly — WithOrgTx/WithUserTx each open their own tx
// and must not be nested (see tenant_tx.go's no-nesting rule). System() delegates
// to the audited WithControlPlane.
func (sc *Scoped) Within(ctx context.Context, fn func(ctx context.Context) error) error {
	// Nesting-safe: if a tx is already on ctx (an outer As().Within or With*Tx),
	// reuse it — the outer scope already established the RLS context, and opening
	// a second tx would deadlock under pool pressure (see tenant_tx.go's
	// no-nesting rule). Lets leaf methods wrap in Within without caring whether a
	// caller already opened a tx.
	if _, hasTx := ctx.Value("tx").(pgx.Tx); hasTx { //nolint:staticcheck // shared "tx" key
		return fn(ctx)
	}
	if sc.id.IsSystem() {
		return sc.store.WithControlPlane(ctx, fn)
	}
	tx, err := sc.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// The connection runs as app_tenant (pool BeforeAcquire hook); we set the
	// identity's RLS vars for the tx lifetime. SET LOCAL semantics via
	// set_config(..., true): cleared on commit/rollback, no leak across reuse.
	if sc.id.UserID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", sc.id.UserID); err != nil {
			return err
		}
	}
	if sc.id.OrgID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", sc.id.OrgID); err != nil {
			return err
		}
	}

	// Same "tx" context key as WithOrgTx/RunInTransaction so existing Store
	// methods pick up the tx through getQueryExecutor without a refactor.
	txCtx := context.WithValue(ctx, "tx", tx) //nolint:staticcheck // shared "tx" key, intentional
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ----------------------------------------------------------------------------
// principals / org slice — first methods migrated onto Scoped (RLS migration
// Step 2). Each runs the existing Store query inside this identity's context, so
// the org-gated tables (organization_members) are visible to a real principal
// instead of failing closed under a bare connection.
// ----------------------------------------------------------------------------

// ListPrincipals lists the principals in this identity's org, RLS enforced.
// kind "" returns all kinds — humans surface via the organization_members join,
// which is RLS-gated on app.current_org_id (supplied by the identity).
func (sc *Scoped) ListPrincipals(ctx context.Context, kind string, pageSize int32, pageToken string) ([]*business.Principal, string, error) {
	var out []*business.Principal
	var next string
	err := sc.Within(ctx, func(ctx context.Context) error {
		var e error
		out, next, e = sc.store.ListPrincipals(ctx, sc.id.OrgID, kind, pageSize, pageToken)
		return e
	})
	return out, next, err
}

// AddOrgMember adds a membership row to this identity's org. organization_members
// is RLS-gated (org_id = app.current_org_id); the identity's OrgID satisfies the
// WITH CHECK, so no bypass is needed for a tenant-scoped caller.
func (sc *Scoped) AddOrgMember(ctx context.Context, userID, role string) error {
	return sc.Within(ctx, func(ctx context.Context) error {
		return sc.store.AddOrgMember(ctx, sc.id.OrgID, userID, role)
	})
}

// GetPrincipal reads a principal by id under this identity's RLS context.
func (sc *Scoped) GetPrincipal(ctx context.Context, id string) (*business.Principal, error) {
	var p *business.Principal
	err := sc.Within(ctx, func(ctx context.Context) error {
		var e error
		p, e = sc.store.GetPrincipal(ctx, id)
		return e
	})
	return p, err
}

// GetAgentPrincipal reads an agent principal under this identity's RLS context.
func (sc *Scoped) GetAgentPrincipal(ctx context.Context, orgID, agentIdentifier string) (*business.Principal, error) {
	var p *business.Principal
	err := sc.Within(ctx, func(ctx context.Context) error {
		var e error
		p, e = sc.store.GetAgentPrincipal(ctx, orgID, agentIdentifier)
		return e
	})
	return p, err
}

// CreateAgentPrincipal inserts an agent principal under this identity's RLS
// context (the org's WITH CHECK is satisfied by a tenant-scoped identity).
func (sc *Scoped) CreateAgentPrincipal(ctx context.Context, p *business.Principal) error {
	return sc.Within(ctx, func(ctx context.Context) error {
		return sc.store.CreateAgentPrincipal(ctx, p)
	})
}

// RevokePrincipal revokes a principal under this identity's RLS context.
func (sc *Scoped) RevokePrincipal(ctx context.Context, id, reason string) error {
	return sc.Within(ctx, func(ctx context.Context) error {
		return sc.store.RevokePrincipal(ctx, id, reason)
	})
}
