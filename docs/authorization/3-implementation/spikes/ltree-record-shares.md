# Spike — hierarchical scope + per-record sharing (`ltree` + `record_shares` + `CheckAccess`)

> **Status: investigation / design draft. Nothing here is wired into the build.**
> This is the concrete form of the two in-scope spikes from
> [`AUTHZ_GAP_ANALYSIS.md`](../../../../AUTHZ_GAP_ANALYSIS.md) (issue #175): (A) typed
> hierarchical scope with precedence, and (B) a per-record cross-owner grant
> primitive. It exists to be reviewed, not merged — the migration is
> deliberately unnumbered-for-real and the Go is a sketch. The "To actually
> land this" section lists the CI/authority obligations a real PR must satisfy.

## What this closes, and how it stays additive

| Gap (from #175) | This spike |
|---|---|
| 1 — no hierarchical / nested scope | `scope_nodes` (ltree registry) + ancestor-match in `CheckAccess` |
| 2 — no per-record cross-owner ACL | `record_shares` overlay + `CheckAccess` |
| 5 — nested/subtree | grant on a subtree root inherits down (same ltree mechanism) |
| 6 — untyped scope taxonomy | `scope_nodes` validates each path's parent on write |

The design principle from the gap analysis holds: **RLS stays the fail-closed
tenant floor; this all sits above it.** `CheckAccess` is deliberately *shaped
like the existing `CheckPermission`* — same subject predicate (principal or
team), same `roles`/`role_permissions` join, same `WithOrgTx` wrapper. The only
new ideas are (a) the scope match becomes an `ltree` **ancestor** test instead
of flat string equality, and (b) a per-record overlay table. In one line:

> **`CheckAccess` = `CheckPermission` with `scope_path @> record_path` instead of `scope = $scope`, plus a `record_shares` union.**

This intentionally does **not** migrate `role_assignments`. Org-wide / flat RBAC
keeps flowing through `CheckPermission` unchanged; hierarchical and per-record
grants are new, parallel tables consulted by the new resolver. Unifying the two
scope columns later is possible but is not required to ship this.

---

## Migration draft

Matches the house style from `migrations/92_org_identity_providers.up.sql`:
`IF NOT EXISTS`, `org_id … ON DELETE CASCADE`, `ENABLE`+`FORCE` RLS with an
`app.current_org_id` policy, then `REVOKE ALL … FROM app_tenant` + exact
`GRANT`s to `app_tenant` and `app_control_plane` (per `DATABASE_AUTHORITY.md`).

```sql
-- NN_layered_access_scopes_and_shares.up.sql
-- (real number is whatever's next; see "To actually land this")
--
-- Hierarchical scope registry + hierarchical role grants + per-record share
-- overlay. All three are TENANT relations: forced RLS on app.current_org_id,
-- exact app_tenant grants, full DML to app_control_plane. RLS here is only the
-- tenant floor — subject resolution happens in CheckAccess's query WHERE clause,
-- exactly as role_assignments does today.

-- ltree gives us materialized-path ancestor tests (@>) with a GiST index.
-- btree_gist lets org_id and scope_path share one GiST index so the tenant
-- filter and the ancestor test are one index probe.
-- Both are "trusted" extensions (installable by the migration principal).
CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ── 1. Typed scope registry (closes gap 6) ───────────────────────────────
-- One row per node in an org's scope tree. Path labels are org-rooted, e.g.
-- 'foundation.solution_42.customer_7'. The parent-exists CHECK is enforced in
-- the write path (see note on ltree labels below), making the taxonomy typed:
-- a scope string that isn't a registered node cannot be granted.
CREATE TABLE IF NOT EXISTS scope_nodes (
    id          UUID  DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id      UUID  NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    scope_path  LTREE NOT NULL,
    kind        TEXT  NOT NULL,          -- product-defined node type, e.g. 'solution','customer'
    label       TEXT  NOT NULL,          -- human display name
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, scope_path)
);
-- Combined GiST: tenant filter + ancestor/descendant operators in one probe.
CREATE INDEX IF NOT EXISTS idx_scope_nodes_path
    ON scope_nodes USING GIST (org_id, scope_path);

-- ── 2. Hierarchical role grants (closes gaps 1 & 5) ──────────────────────
-- "principal (or team) holds role at this scope node." A grant at an ancestor
-- node inherits to the whole subtree via the @> ancestor test in CheckAccess.
-- role_id reuses the existing roles/role_permissions machinery, so a grant's
-- capabilities are resolved through the same (resource, action) rows as RBAC.
CREATE TABLE IF NOT EXISTS scope_grants (
    id            UUID  DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id        UUID  NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    subject_id    UUID  NOT NULL,                 -- principal id OR team id
    subject_kind  TEXT  NOT NULL CHECK (subject_kind IN ('principal', 'team')),
    scope_path    LTREE NOT NULL,
    role_id       UUID  NOT NULL REFERENCES roles(id),
    granted_by    UUID  REFERENCES principals(id),
    expires_at    TIMESTAMPTZ,                     -- NULL = standing
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, subject_id, subject_kind, scope_path, role_id)
);
CREATE INDEX IF NOT EXISTS idx_scope_grants_subject
    ON scope_grants (org_id, subject_id, subject_kind);
CREATE INDEX IF NOT EXISTS idx_scope_grants_path
    ON scope_grants USING GIST (org_id, scope_path);

-- ── 3. Per-record share overlay (closes gap 2) ───────────────────────────
-- "subject may act on THIS specific record, across the ownership boundary."
-- resource_type is the same vocabulary as role_permissions.resource. resource_id
-- is opaque to the starter (the product's row id). This is the durable ACL that
-- delegation_grants deliberately is not.
CREATE TABLE IF NOT EXISTS record_shares (
    id            UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    subject_id    UUID NOT NULL,
    subject_kind  TEXT NOT NULL CHECK (subject_kind IN ('principal', 'team')),
    role_id       UUID NOT NULL REFERENCES roles(id),
    granted_by    UUID REFERENCES principals(id),
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, resource_type, resource_id, subject_id, subject_kind, role_id)
);
CREATE INDEX IF NOT EXISTS idx_record_shares_subject
    ON record_shares (org_id, subject_id, subject_kind);
CREATE INDEX IF NOT EXISTS idx_record_shares_resource
    ON record_shares (org_id, resource_type, resource_id);

-- ── RLS: tenant floor only (subject filtering is in CheckAccess) ─────────
ALTER TABLE scope_nodes    ENABLE ROW LEVEL SECURITY;
ALTER TABLE scope_nodes    FORCE  ROW LEVEL SECURITY;
ALTER TABLE scope_grants   ENABLE ROW LEVEL SECURITY;
ALTER TABLE scope_grants   FORCE  ROW LEVEL SECURITY;
ALTER TABLE record_shares  ENABLE ROW LEVEL SECURITY;
ALTER TABLE record_shares  FORCE  ROW LEVEL SECURITY;

CREATE POLICY scope_nodes_tenant ON scope_nodes
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
CREATE POLICY scope_grants_tenant ON scope_grants
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
CREATE POLICY record_shares_tenant ON record_shares
    USING      (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

-- ── Exact grants (migration 63/67 convention) ────────────────────────────
REVOKE ALL PRIVILEGES ON scope_nodes, scope_grants, record_shares FROM app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_nodes   TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_grants  TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON record_shares TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_nodes   TO app_control_plane;
GRANT SELECT, INSERT, UPDATE, DELETE ON scope_grants  TO app_control_plane;
GRANT SELECT, INSERT, UPDATE, DELETE ON record_shares TO app_control_plane;
```

Down migration is the mirror: `DROP TABLE … CASCADE` for the three tables (leave
the extensions — other things may use them).

---

## `CheckAccess` — Go sketch

Mirrors `pkg/infra/postgres_permissions.go:CheckPermission` (dynamic subject
predicate, single query, `LIMIT 1`) and `pkg/business/service.go:CheckPermission`
(the `WithOrgTx` wrapper). The store method resolves in **one** query: a `UNION`
of ancestor scope-grants and per-record shares, both joined through
`roles`/`role_permissions` so capability resolution is identical to RBAC.

```go
// pkg/infra/postgres_layered_access.go  (sketch)

// CheckAccess reports whether subject may perform (resource, action) on a
// specific record, via either a hierarchical scope grant at any ancestor of the
// record's scope_path, or a direct per-record share. It is the hierarchical +
// per-record companion to CheckPermission; org-wide/flat capability still comes
// from CheckPermission. recordPath is the record's own scope node (the product
// resolves it from its RLS-protected row before calling).
func (s *PostgresStore) CheckAccess(
	ctx context.Context,
	subjectID string, subjectKind gen.SubjectKind,
	resourceType, action string,
	orgID, resourceID, recordPath string,
) (bool, string, error) {
	w := wool.Get(ctx).In("CheckAccess")
	executor := s.getQueryExecutor(ctx)

	// Same subject shape as CheckPermission: a human principal also inherits
	// grants assigned to teams they belong to.
	var subjectPredicate string
	switch subjectKind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		subjectPredicate = `(
			(%[1]s.subject_kind = 'principal' AND %[1]s.subject_id = $1)
			OR (%[1]s.subject_kind = 'team' AND %[1]s.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1))
		)`
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		subjectPredicate = `(%[1]s.subject_kind = 'team' AND %[1]s.subject_id = $1)`
	default:
		return false, "", fmt.Errorf("check access: unsupported subject kind %s", subjectKind)
	}

	// $1 subject, $2 resource(type), $3 action, $4 recordPath (ltree), $5 resourceID.
	// Ancestor branch: a grant whose scope_path is an ancestor-or-equal of the
	// record's path (g.scope_path @> $4). Overlay branch: a direct share on the id.
	// A NULL role_permissions wildcard ('*') matches, same as CheckPermission.
	query := fmt.Sprintf(`
		SELECT 'scope' AS via
		FROM scope_grants g
		JOIN role_permissions rp ON rp.role_id = g.role_id
		WHERE `+fmt.Sprintf(subjectPredicate, "g")+`
		  AND g.scope_path @> $4::ltree
		  AND (g.expires_at IS NULL OR g.expires_at > now())
		  AND (rp.resource = '*' OR rp.resource = $2)
		  AND (rp.action   = '*' OR rp.action   = $3)
		UNION ALL
		SELECT 'share' AS via
		FROM record_shares s
		JOIN role_permissions rp ON rp.role_id = s.role_id
		WHERE `+fmt.Sprintf(subjectPredicate, "s")+`
		  AND s.resource_type = $2
		  AND s.resource_id   = $5
		  AND (s.expires_at IS NULL OR s.expires_at > now())
		  AND (rp.resource = '*' OR rp.resource = $2)
		  AND (rp.action   = '*' OR rp.action   = $3)
		LIMIT 1`)

	var via string
	err := executor.QueryRow(ctx, query, subjectID, resourceType, action, recordPath, resourceID).Scan(&via)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "no scope grant or record share", nil
		}
		return false, "", w.Wrapf(err, "failed to check access")
	}
	return true, "granted via " + via, nil
}
```

Service wrapper, identical shape to `Service.CheckPermission`:

```go
// pkg/business/service.go  (sketch)
func (s *Service) CheckAccess(ctx context.Context, req *gen.CheckAccessRequest) (bool, string, error) {
	var allowed bool
	var reason string
	wrap := func(ctx context.Context) error {
		a, r, err := s.store.CheckAccess(ctx,
			req.SubjectId, req.SubjectKind,
			req.ResourceType, req.Action,
			req.OrgId, req.ResourceId, req.RecordPath)
		allowed, reason = a, r
		return err
	}
	// Always org-scoped — a record lives in exactly one tenant.
	if err := s.store.WithOrgTx(ctx, req.OrgId, wrap); err != nil {
		return false, "", err
	}
	return allowed, reason, nil
}
```

Handler usage stays honest about the layering — call `CheckPermission` for the
org-wide capability first (cheap, flat), fall through to `CheckAccess` for the
hierarchical/shared case:

```go
// allow if the caller has org-wide capability OR a scope/record grant
ok, _, _ := svc.CheckPermission(ctx, permReq)          // flat RBAC, existing
if !ok {
	ok, _, _ = svc.CheckAccess(ctx, accessReq)          // ltree + record_shares, new
}
```

---

## Visibility / list resolver (most-specific-wins)

`CheckAccess` answers "can I touch this one record." Listing needs the dual: the
set of records a caller may see, with the **most-specific grant winning** when
the same node is reachable at several depths. That's the `nlevel() DESC` +
`DISTINCT ON` query — run it against the product's table (which carries a
`scope_path ltree` column), still under the RLS tenant floor:

```sql
SELECT DISTINCT ON (a.id) a.id, g.role_id, g.scope_path AS granted_at
FROM   <product_table> a
JOIN   scope_grants g
  ON   g.org_id = a.org_id
  AND  <subjectPredicate on g>
  AND  g.scope_path @> a.scope_path        -- ancestor-or-equal, GiST-indexed
WHERE  a.org_id = current_setting('app.current_org_id', true)::uuid
ORDER  BY a.id, nlevel(g.scope_path) DESC;  -- deepest grant wins
-- UNION the record_shares overlay for shared-but-not-in-hierarchy rows.
```

This is the "default-deny visibility filter applied to metadata before content
loads" pattern the Drive/GitHub/Notion references all use.

---

## Integration points

- **`CheckPermission` is untouched.** Flat/org-wide RBAC keeps working; this is
  strictly additive. No `role_assignments` migration.
- **Capability vocabulary is shared.** `scope_grants.role_id` / `record_shares.role_id`
  point at the same `roles` → `role_permissions` rows, so "what can this role do"
  has one definition across flat and hierarchical grants.
- **`MethodPolicy` fit.** An RPC over hierarchical data would declare its
  `resource:action` as today; the handler resolves the record's `scope_path`
  from its own row (analogous to how `resource_bindings` resolve id→org) and
  calls `CheckAccess`. No change to the descriptor compiler is required for the
  handler-side path.
- **RLS-side composition is optional and later.** You *can* push the ancestor
  test into an RLS policy on the product table, but that needs the acting
  **principal** available as a GUC (see open questions). Handler-side
  `CheckAccess` needs no new GUC and is the recommended first step.

---

## Open questions & decisions (investigation)

1. **ltree labels vs. UUIDs — real gotcha.** `ltree` path labels are restricted
   (historically `[A-Za-z0-9_]`; newer Postgres widened the set but hyphens are
   still not universally safe across versions). Raw UUIDs contain hyphens and
   **cannot** be used as path labels as-is. Decide the label encoding: a slug,
   a hyphen-stripped hex, or a short synthetic key per node. `scope_nodes.id`
   stays a real UUID; `scope_path` uses the encoded labels. This needs settling
   before any schema is real.
2. **No `app.current_principal_id` GUC today.** Only `app.current_org_id` and
   `app.current_user_id` exist. Handler-side `CheckAccess` sidesteps this
   (subject is a query parameter). RLS-side composition would need a principal
   GUC — and note humans have `principal.id == user.uuid` while agents/services
   don't, so it can't just alias `current_user_id`. Add `app.current_principal_id`
   only if/when we go RLS-side.
3. **Role model: reuse vs. a viewer/editor/owner ladder.** This sketch reuses
   `roles`/`role_permissions` (consistent, one vocabulary). The Drive/Notion
   "highest-permission-wins" rule then means "any matching grant that permits the
   action" — which the `UNION … LIMIT 1` already gives for a boolean check. If we
   ever need to *return* the effective role (not just allow/deny), add the
   `nlevel() DESC` ordering to pick the most-specific, and define whether
   most-specific or highest-privilege wins on conflict.
4. **Reduce-at-leaf semantics.** Drive's rule is "inherited access can be
   widened at a leaf but not silently narrowed." This overlay is widen-only
   (grants add). If we ever need per-record *denials*, that's a separate,
   conflict-resolution-heavy feature — recommend explicitly out of scope for v1.
5. **Performance.** The GiST `@>` ancestor test is fast; the risk is the
   list-resolver's correlated cost on wide result sets. If profiling shows it,
   precompute the caller's entitled ancestor paths once per request and pass them
   as an array (`a.scope_path <@ ANY($paths)`). Start with the join for
   correctness; optimize only if measured.
6. **Graduation trigger.** If group-of-groups nesting or folder-of-folders depth
   grows past what recursive SQL serves comfortably, this is the inflection point
   to move `scope_grants`/`record_shares` behind a ReBAC PDP (SpiceDB/OpenFGA/
   WorkOS FGA) — keeping RLS as the floor and adopting the consistency-token
   discipline. Until then, these tables are the right amount of machinery.
7. **Chain-authorship linkage (separate spike).** `granted_by` records who
   created a grant/share; tying that to the Work Context `actor_chain` /
   `delegation_grants` (so a share made *on behalf of* someone is provable) is
   the durable-actor-chain spike, not this one.

---

## To actually land this (not done here — investigation only)

Per `DATABASE_AUTHORITY.md`'s fail-closed migration rule and the CI gates, a
real PR must additionally:

- Give the migration its **real next number**, add the **down** migration, and
  regenerate **`module/tools/base-manifest.json`** from a clean worktree (base-integrity gate).
- Classify the three tables as **tenant** relations in the executable
  scope/RLS inventory and the **exact grant matrix**, or the accounts infra
  suite's "every public table is classified + forced-RLS + granted" gate fails
  (the "Enforce tenant RLS coverage" CI step).
- Add infra integration tests: cross-tenant blocking + fail-closed-on-unwrapped
  for each table (the `RLS_PLAN.md` test pattern), plus `CheckAccess` unit tests
  for ancestor inheritance, per-record share, team inheritance, expiry, and
  wildcard `role_permissions`.
- Confirm the connection pool runs **transaction mode** so `SET LOCAL`/`set_config(..., true)`
  can't leak `app.current_org_id` across pooled requests (the top multi-tenant
  RLS footgun; `AUTHZ.md` says per-tx `SET LOCAL` is already used — this just
  re-confirms the pool mode).
- Decide the ltree label encoding (open question 1) and the `CheckAccessRequest`
  proto shape before generating anything.
