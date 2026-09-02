# ADR 0007: Dashboard storage and ownership — configuration in a dedicated store, preferences in user settings

- Status: Proposed
- Date: 2026-09-02
- Task: gates the *User-owned, runtime-editable dashboards* epic (#369). Fixes,
  as a deliberate decision, **where dashboards and their preferences live** so
  that #365 (persistence) and #366 (user model) implement against a settled
  model instead of assuming one. It does not change runtime behavior; it names
  the store the already-shipped frontend seams
  (`DashboardRecord` / `DashboardLibrary`, #366; `DashboardDraftStore`, #365)
  drop onto. Ties to #367 (authz) for the read-only, org-scoped invariant.

## Context

A "dashboard" reads like one object but is **two things with different
lifecycles and owners**:

1. **Configuration** — the dashboard *definition*: events → metrics → widgets →
   layout. Authored, named, shared, versioned; potentially org-wide. This is
   the `spec` (`DashboardDef`) plus the lifecycle metadata a bare spec literal
   never had — id, name, owner, tenant, visibility, timestamps.
2. **Preferences** — per-*user* view state layered over a dashboard: which
   dashboard is my default, my layout/theme overrides, collapsed sections, my
   time-range presets. Personal, never shared, meaningless to another user.

Conflating them — one JSONB blob per user — is the tempting shortcut and the
wrong one: it makes sharing and org governance impossible (a shared board can't
live inside one user's private blob) and it forces personal view state to be
co-owned by whoever owns the definition. The split is the load-bearing
decision; everything else follows from it.

The pieces this decision must fit already exist and constrain it:

- **The config record is already modeled** (#366). `DashboardRecord` is
  `{ id, name, spec, visibility: "private" | "org", createdAt, updatedAt }`,
  with `orgId` / `ownerId` deliberately *not* in the client shape because "the
  browser can't attest to tenancy or ownership" — they are assigned by the
  server that persists the record. `DashboardLibrary` is its CRUD seam, whose
  own contract already anticipates "the eventual server-backed store
  (org-scoped, RLS, with ownership and cross-user org-shared reads)." That
  shape *is* storage option (A); this ADR ratifies it rather than inventing it.
- **The template/baseline scope already ships** as the solution-manifest
  `dashboard` slot (a `DataGraph` in `saas-plugin-manifest`) — e.g. lastlogin's
  "Activity". It is read-only at runtime by construction: it is delivered in a
  manifest, not a table.
- **The typed settings system is built and green** (SETTINGS.md): per-user
  settings are a composed, sparse ProtoJSON document in `users.settings`,
  written through `@codefly/saas-settings` / `pkg/settings`. Crucially,
  **`org_settings` is fixed-column, not composed JSON**, and settings model a
  *singular config document*, not *collections* — so "a list of shareable
  dashboards" is a poor fit for settings, while "one user's sparse view-state
  document" is exactly what settings already are.
- **The #367 invariant is fixed:** whatever the store, a spec is
  validated on read *and* write and can only ever compile to **org-scoped,
  read-only** audit queries. The data-plane half already fails closed:
  `compileMetric` throws "cannot compile an org-scoped audit query without a
  viewer org" on a blank context org (#374). Storage must not open a hole in
  that — a persisted spec is untrusted input re-validated on the way out.

## The config/prefs split — the decision that forces the rest

| | **Configuration** (the dashboard) | **Preferences** (the view) |
| --- | --- | --- |
| What | events → metrics → widgets → layout (`spec`) + lifecycle | default choice, layout/theme overrides, collapsed sections, time presets |
| Owner | a solution, an org, or a user | always exactly one user |
| Shared? | can be (org, or read-only baseline) | never |
| Shape | a **collection** of first-class objects | one **sparse document** per user |
| Lifecycle | create / rename / share / fork / delete / version | set / clear a field; follows the user, not the board |
| Natural home | **dedicated dashboards store** | **`user_settings`** (composed JSONB) |

Two independent axes fall out of the split and must not be confused:

- **Ownership** is a property of a *configuration* record: who may edit it and
  which tenant governs it.
- **Preference** is a *user*'s layer over any configuration, whatever its
  owner. My default and my layout of the **org** security board are *my*
  preferences over a board I do not own and cannot edit.

## Ownership scopes and default ownership

| Scope | Owner | Editable at runtime by | Backing |
| --- | --- | --- | --- |
| **Solution / template** | a solution (shipped in its manifest) | no one — read-only baseline | solution manifest `dashboard` slot (already shipped) |
| **Org** | the organization | admins / holders of `dashboards:share` | dashboards store, `scope = org` |
| **User** | one user | that user | dashboards store, `scope = user` |
| **User preferences** | one user, *layered over* any of the above | that user | `user_settings` composed field |

**Default ownership: user-private, promotable to org.** A newly authored
dashboard is `scope = user`, `visibility = private` — matching the shipped
`DEFAULT_DASHBOARD_VISIBILITY = "private"`. Rationale: authoring is an
experiment until it is worth sharing; private-by-default is the least-surprise,
least-blast-radius choice and needs no elevated permission to start. Promotion
to org (share) is the privileged, audited transition — a `dashboards:share`
check, not a default. "Org-first" is rejected: it would demand share
permission to scribble a personal board and make every draft org-visible.

**Fork is a first-class, supported operation, and it is the *only* way a
non-owner "edits" a shared or baseline board.** Forking a solution/template or
org dashboard produces a new `scope = user`, `visibility = private` record with
a **copied spec** and a provenance pointer (`forkedFrom`) to the source id.
This already composes on the shipped `DashboardLibrary` surface — "`duplicate`
is `create` with a copied spec" — so fork widens no interface. The baseline and
the org original stay untouched and read-only; the fork is the user's to edit.

## Layer composition and precedence at render time

A rendered dashboard is the resolution of up to four layers, applied in a fixed
order, **last writer wins per field**:

```
solution baseline  ←  org dashboard  ←  user dashboard / fork  ←  user preferences
   (read-only)          (shared)          (owned copy)            (personal view)
```

- The first three are **configuration** and are mutually exclusive *as the
  active definition*: you render exactly one dashboard record (or one baseline)
  as the base — you do not merge a solution spec and an org spec into one
  hybrid definition. "Precedence" among them means **which one you opened /
  chose as default**, resolved through the ownership/visibility rules above
  (a fork supersedes its source *for that user*; it does not mutate it).
- **User preferences are the only layer that overlays**, and it overlays
  *non-destructively*: it can reorder, collapse, retheme, and pick a default
  time range **on top of** whichever configuration is active, but it can never
  add a metric, change a filter, or alter what data a widget queries. Anything
  that changes *what is measured* is configuration and belongs in a record the
  user owns (their own board, or a fork) — never in preferences. This keeps the
  #367 invariant intact: preferences cannot smuggle a query past validation
  because preferences carry no queryable spec.

## Storage backing — the decision

**Adopt the hybrid (issue option C): configuration in a dedicated store,
preferences in `user_settings`.**

### Configuration — a dedicated dashboards store (option A)

A new first-class object with its own proto, table, and lifecycle:

```
dashboard {
  id          uuid  primary key
  org_id      uuid  not null        -- tenant; RLS partition key
  owner_id    uuid  null            -- the owning user for scope = user; null for scope = org
  scope       enum  (user | org)    -- solution/template is not stored; it lives in the manifest
  name        text  not null
  spec        jsonb not null        -- DashboardDef, validated on read AND write
  visibility  enum  (private | org)
  forked_from uuid  null            -- provenance for a fork; references dashboard.id
  version     int   not null        -- optimistic concurrency + spec-schema version
  created_at  timestamptz not null
  updated_at  timestamptz not null
}
```

- **Org-scoped with RLS**, exactly like every other tenant table here.
- **Read policy:** a member reads their own `scope = user` records *and* every
  `scope = org, visibility = org` record in their org. This is the
  "cross-user org-shared reads" the shipped `DashboardLibrary` comment already
  names.
- **`orgId` / `ownerId` are server-assigned** — never trusted from the client,
  matching why `DashboardRecord` omits them from the client shape.
- **`spec` is re-validated on read**, not just on write: a stored spec is
  untrusted input, and #367 requires it compile only to org-scoped read-only
  queries regardless of how it got into the row (`assertDashboardSpec` is
  already the shared write-boundary validator; it becomes a read-boundary one
  too).
- The solution/template scope is **not** a row — it stays in the manifest,
  which is what makes it an un-editable baseline for free.

Why not settings (option B) for configuration: `org_settings` is fixed-column,
so an org board could not live there without a schema change per board; and
settings model one document, not a shareable collection with per-object
visibility and ownership. Sharing, org governance, list/create/fork, and RLS
are all native to a table and alien to a settings blob.

### Preferences — a composed field on `user_settings` (the existing machinery)

Per-user view state is a new **composed settings field**
(`dashboards.preferences` or similar) written through `@codefly/saas-settings`,
landing as sparse ProtoJSON in `users.settings` — the same path
`appearance.theme` and the notification toggles already take. It carries the
default-dashboard id, per-dashboard layout/theme/collapsed overrides, and
time-range presets, **keyed by dashboard id** so a user's view of the org board
is stored against that board's id without touching the board.

Why settings and not the dashboards store: preferences are singular per user,
never shared, and are exactly the "one sparse per-user document" settings were
built for. Reusing the machinery gets typing, composition, and the per-user
transactional write for free, and keeps personal state out of the shareable,
org-governed config table.

## Authz mapping (ties to #367)

| Operation | Scope acted on | Check |
| --- | --- | --- |
| List / read own | user | authenticated; RLS scopes to owner + org |
| Read org board | org | authenticated member; RLS scopes to org |
| Create / rename / edit / delete own | user | owner of the record (fails closed on non-owner) |
| **Fork** a baseline or org board | creates a user record | authenticated member (read on source) |
| **Share** (promote user → org, set `visibility = org`) | org | `dashboards:share` (admin-tier), audited |
| Edit / unshare an org board | org | `dashboards:share` |
| Render (compile spec → query) | any | org-scoped, read-only audit query only (#367 / #374 invariant) |

Enforcement lives in the accounts **handler layer**, not the sidecar
(`requireScope` / owner checks), consistent with the platform's
"sidecar stamps identity, handler enforces permission" split. The concrete
scope wiring and rate limits are #367's to implement; this ADR fixes the
*mapping* they implement against.

## Options considered

| Option | Sketch | Verdict |
| --- | --- | --- |
| **A. Dedicated store only** | config *and* prefs as rows in the dashboards table | **Rejected as the whole answer** — right for config, wrong for personal view-state, which is singular-per-user and already has a home in `user_settings`; would duplicate the settings machinery and co-locate personal state with shared config |
| **B. Settings-composed only** | dashboards as a composed field on `user_settings` / `org_settings` | **Rejected** — `org_settings` is fixed-column (no place for an org board without a per-board schema change); settings model a singular document, not a shareable collection with per-object ownership, visibility, and RLS |
| **C. Hybrid — config in a dedicated store, prefs in `user_settings`** *(proposed)* | first-class dashboards table (A) for definitions/sharing/lifecycle; composed `user_settings` field for per-user view state | **Accepted** — matches the config/prefs split exactly; config gets tables' native sharing/RLS/lifecycle, prefs get settings' native per-user composition; both existing seams (#365/#366 frontend, typed settings) drop straight on |

## Consequences

- **#366** implements `DashboardLibrary` as a server-backed store over the
  `dashboard` table above; the localStorage/in-memory implementations already
  shipped are the drop-in placeholders behind the same interface.
- **#365** persists a draft/spec through that store; the `DashboardDraftStore`
  seam and its `dashboardRecordStore` / `driverDashboardStore` adapters already
  target a record id, so no editor code changes when the backing becomes the
  server.
- **#367** wires the authz-scope table above and the render invariant; nothing
  here relaxes the "org-scoped, read-only" compile guarantee — a stored spec is
  re-validated on read.
- **Preferences** need a new composed `user_settings` field and its typed
  catalog entry, following SETTINGS.md; no new store, no `org_settings` change.
- A **fork** is `create` with a copied spec plus `forked_from`; the baseline
  and org original stay read-only. No mutation path to a solution/template
  dashboard is introduced — it remains manifest-delivered.
- New moving parts are confined to the config store: a proto, a migration
  (org-scoped table + RLS + read policy for org-shared visibility), and the
  handler-layer owner/`dashboards:share` checks. Preferences add only a
  settings field.

## References

- #369 — User-owned, runtime-editable dashboards (epic). #365 — persistence;
  #366 — user model; #367 — authz / rate limits.
- `services/frontend/code/src/features/dashboard/model/record.ts` and
  `.../service/dashboard-library.ts` — the shipped `DashboardRecord` /
  `DashboardLibrary` shape and its "eventual server-backed, org-scoped, RLS"
  contract (#366).
- `services/frontend/code/src/features/dashboard/service/draft-store.ts` — the
  `DashboardDraftStore` seam (#365).
- `services/frontend/code/packages/saas-sdk/src/datagraph/compile.ts` — the
  fail-closed org-scoped compile guard (#367 / #374 invariant).
- [SETTINGS.md](../../SETTINGS.md) — typed settings: `user_settings` composed
  sparse ProtoJSON vs. fixed-column `org_settings`; the `@codefly/saas-settings`
  runtime.
- `services/frontend/code/packages/saas-plugin-manifest/src/manifest.ts` — the
  solution-manifest `dashboard` slot: the read-only template/baseline scope.
