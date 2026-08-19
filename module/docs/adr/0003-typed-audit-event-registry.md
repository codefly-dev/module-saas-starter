# ADR 0003: Typed audit-event registry — single-table STI, a code-owned catalog, and in-Postgres analytics

- Status: Proposed
- Date: 2026-08-18
- Task: closes the review-and-planning deliverable of #170. Implementation is
  deferred to follow-up tickets that this ADR authorizes but does not open.

## Context

The audit subsystem records who did what, to which resource, in which tenant,
and when. Today it is a single loosely-typed table with a free-form `action`
string and an untyped `metadata` blob. #170 asks for a full review and a design
plan that evolves it into a **registered, typed event model** supporting
structured search (per user / tenant / type / resource / time) and
analytics/aggregation, while preserving the existing guarantees: append-only
immutability, tenant RLS, retention, and the opt-in S3 export contract.

This ADR is the deliverable. It recommends an approach with an explicit
pros/cons comparison, a schema and registry format, an indexing/partitioning
and retention plan, an RLS/immutability preservation strategy, a migration and
backfill plan, an API/UI sketch, and an explicit stance on where analytics
lives. It does not change code.

### Baseline (what exists today)

Source of truth is `services/store/migrations`, the accounts business layer,
and the accounts infra layer.

- **Schema** — `7_create_audit_events.up.sql`:
  `audit_events(id UUID PK, actor_id UUID, actor_type TEXT CHECK(user|api_key|system),
  action TEXT, resource TEXT, resource_id TEXT, org_id UUID,
  metadata JSONB DEFAULT '{}', ip_address TEXT, created_at TIMESTAMPTZ)`.
  `org_id` is nullable: tenant events carry a concrete org, system events
  (cron, GDPR ops, unresolved-org login failures) carry `NULL`.
- **Immutability** — `7_create_audit_events.up.sql`: `audit_events_immutable()`
  raises unconditionally; `BEFORE UPDATE` and `BEFORE DELETE … FOR EACH ROW`
  triggers make every row-level mutation fail. There is **no** GUC or role
  escape hatch inside the trigger.
- **RLS** — `31_rls_audit_events.up.sql`: `ENABLE` + `FORCE`, one polymorphic
  per-`org_id` policy, fail-closed. `USING`/`WITH CHECK` are symmetric: a row is
  visible/writable iff `app.bypass='1'` **or** `org_id = current_setting('app.current_org_id')`.
  NULL-org (system) rows are reachable only under bypass, i.e. through the
  control-plane path.
- **Write path** — `business/audit.go` `DurableAuditEmitter.Emit`: assigns a
  uuid v7 id and timestamp, then in **one** transaction inserts the audit row
  and every matching webhook-outbox row. Tenant events go through `WithOrgTx`;
  NULL-org events through `WithControlPlane`. Webhook fan-out is keyed on
  `entry.Action` (`GetActiveWebhookSubscriptions(action)`), so the free-form
  `action` string is already load-bearing beyond audit.
- **Read path** — `infra/postgres_audit.go` `QueryAuditLog`: dynamic `WHERE`
  over the fixed columns only (`org_id`, `actor_id`, `action`, `resource`,
  `resource_id`, `from`/`to`), keyset pagination on `(created_at, id)` under a
  stable `ORDER BY created_at DESC, id DESC`. No `metadata` filtering, no
  aggregation. `metadata` is modeled in Go as `map[string]string` — flatter
  than arbitrary JSON.
- **Indexes** — `7_create_audit_events.up.sql`: `(org_id, created_at DESC)`,
  `actor_id`, `action`, `(resource, resource_id)`. No `(org_id, action/type,
  created_at)` composite and no GIN on `metadata`.
- **Retention** — seeded in `17_…_retention_…up.sql`
  (`data_retention_policies('audit_events', 365)`); executed by
  `business/retention.go` `RunRetention` → `infra/postgres_retention.go`
  `DeleteOldAuditEvents` as `DELETE FROM audit_events WHERE created_at < $1`
  under `WithControlPlane`.
- **Export** — `business/audit_exporter.go`: opt-in per-org export to the
  **customer's own** S3 (`19_audit_export_configs.up.sql`), driven off the
  shared `QueryAuditLog` in 5k-row pages, cursor advanced by
  `last_exported_at`. Wire shape is one JSON object per line (JSONL): `id,
  actor_id, actor_type, action, resource, resource_id, org_id, metadata,
  ip_address, created_at` (see `auditExportEntry` in `business/audit_export.go`).
  This is a downstream copy, not a store.

### Review findings that shape the decision

Two are pre-existing correctness issues the redesign must not inherit; the rest
are the gaps #170 names.

1. **Retention silently collides with the immutability trigger.** `RunRetention`
   wraps the delete in `WithControlPlane`, which sets `app.bypass='1'`. That GUC
   governs the **RLS policy**, not the `audit_events_no_delete` trigger — the
   trigger raises `'audit_events table is append-only'` regardless. The error
   propagates to `RunRetention`, which logs a warning and `continue`s to the
   next policy. Net effect: **audit retention deletes nothing today**, and there
   is no test over `DeleteOldAuditEvents`/`RunRetention` for `audit_events` to
   catch it. Any redesign must reconcile "immutable rows" with "bounded
   retention" deliberately rather than leave a delete path that cannot run.
2. **The trigger also blocks backfill.** Because `BEFORE UPDATE` raises too, an
   in-place `UPDATE … SET event_type = …` over existing rows is impossible
   without suspending the trigger. The migration plan has to account for this.
3. **No registry.** `action` is a free-form string. In practice ~27 dotted
   names exist (`user.registered`, `api_key.created`, `role.assigned`,
   `auth.mfa_challenge_completed`, …) but nothing declares, owns, versions, or
   validates them. A typo mints a new "event type" silently, and — because
   webhook fan-out keys on `action` — a typo also silently drops webhook
   deliveries.
4. **No payload typing.** `metadata` is `map[string]string` with no per-type
   schema. Consumers guess keys; producers can drift; no field can be marked
   PII for redaction.
5. **Weak search/analytics.** Only fixed-column equality + time range. No
   type-scoped composite index, no payload search, and no aggregation surface
   at all ("do math on this").

### Relevant precedents already in the codebase

- **A code-owned catalog projected to DB + TypeScript already exists.**
  `business/service_vocabulary.go` declares permissions and entitlements as Go
  slices that are "the one vocabulary," with database parity tests and generated
  TypeScript as *projections* of that list "rather than parallel inventories."
  This is the exact shape the event registry should take, so we are not
  inventing a new pattern.
- **An analytics schema already exists in Postgres.** `pkg/metrics/warehouse_schema.sql`
  defines `measurement.metric_values(metric_key, observed_at, value, dimensions
  jsonb, …)`. Aggregation is already expected to live in PG, not a separate OLAP
  store.
- **A durable job platform exists** (used by the exporter and webhook outbox),
  so any scheduled rollup refresh has a home without new infra.
- **IDs are uuid v7** (`13_production_ready_v7`), so the PK is already
  time-correlated — friendly to keyset paging and time-partition pruning.

The guiding constraint from the platform direction is **keep infra minimal
(Postgres + Redis)**. That materially narrows the analytics options below.

## Decision

Adopt **Direction A (single-table STI + a typed registry + validated JSONB
payload)** as the foundation, with the registry **owned in Go code** (mirroring
`service_vocabulary.go`) and projected to a Postgres reference table and to
generated TypeScript. Adopt the **analytics half of Direction E strictly inside
Postgres** — and only when volume demands it — via materialized rollups in the
existing `measurement` schema. Reject B, C, and D. Do **not** add Timescale or
any external OLAP store; that stays a documented escape hatch, not the plan.

In one line: **A now, E-in-Postgres later, no new infra.**

### Options considered

| Option | What it is | Pros | Cons | Verdict |
|---|---|---|---|---|
| **A. Single table + discriminator (STI) + typed registry + validated JSONB** *(proposed)* | One `audit_events` table, `event_type` discriminator, core columns promoted for indexing, per-type payload in JSONB validated against a registered schema | One write path & one read path; trivial cross-type queries and time-series math; immutability/RLS/retention stay table-level; GIN-indexable payload; incremental extension of today's table | Wide sparse rows; JSONB validation must be enforced in the app (drift risk); per-type search needs partial/expression indexes; single hot table at very high volume | **Accepted** (with time partitioning to bound the last con) |
| **B. EAV (entity-attribute-value)** | One row per attribute value | Fully dynamic attributes | Query/aggregation hell, no type safety, poor performance; hostile to "do math"; RLS/immutability multiply across attribute rows | **Rejected** — anti-pattern for this workload |
| **C. Class-table inheritance (core + per-type child tables)** | Core table + a child table per event type | Strong per-type typing; narrow tables; FK integrity | Cross-type queries need union/join; a migration + new RLS + new immutability triggers *per new event type*; time-series math across types is painful; export must fan across N tables | **Rejected** — moving-parts cost per type is not worth it for an append-only log |
| **D. JSONB-only, status quo improved** | Keep free-form `action`, add indexes + a code-side name list | Minimal change; flexible | No enforced schema/versioning; typo-prone names (also breaks webhook fan-out); weak analytics guarantees | **Rejected as the endpoint** — but its indexing improvements are folded into A |
| **E. Hybrid: STI core + analytics projection** | A as source of truth **plus** a derived projection (materialized views / continuous aggregates / rollup tables / OLAP) | Clean OLTP audit + fast aggregates without hammering the immutable table; scales "math" | Projection freshness/consistency; extra infra if OLAP | **Partially accepted** — take the projection idea, keep it **inside Postgres**, defer until a measured need |

**Why A over C** (the only serious alternative): the audit log is a single
append-only stream whose defining queries are *cross-type* ("everything user X
did", "all events in org Y last week", "count by type over time"). C optimizes
the rarer per-type-in-isolation case and taxes every cross-type query and every
new event type. A keeps one write path, one read path, and one place to enforce
immutability/RLS/retention — and its weak spot (one hot table) is exactly what
range partitioning fixes.

**Why the registry lives in Go, not only in the DB:** the codebase already
treats a Go catalog as the single source of truth and the DB/TS as projections
(`service_vocabulary.go`). Producers reference typed constants at compile time
(catching the typo class of bug that today silently breaks webhook fan-out), the
DB reference table gives the discriminator FK integrity and join-ability, and
the FE consumes the generated projection. One source, three consumers — no
parallel inventories.

### Schema

Keep one physical table. Additive columns, no column drops during rollout.

```sql
-- New: the registry projection. Global (no org_id); seeded from the Go catalog.
CREATE TABLE audit_event_types (
    name            TEXT PRIMARY KEY,             -- e.g. 'user.registered'
    version         INT  NOT NULL DEFAULT 1,      -- current payload schema version
    category        TEXT NOT NULL,                -- 'identity'|'access'|'billing'|'security'|'system'|...
    owner           TEXT NOT NULL,                -- owning domain/team
    payload_schema  JSONB NOT NULL,               -- JSON Schema for the payload at `version`
    deprecated      BOOLEAN NOT NULL DEFAULT FALSE
);

-- Extend the existing table (illustrative; real DDL is split across migrations).
ALTER TABLE audit_events
    ADD COLUMN event_type     TEXT REFERENCES audit_event_types(name),
    ADD COLUMN schema_version INT NOT NULL DEFAULT 1,
    ADD COLUMN payload        JSONB NOT NULL DEFAULT '{}'::jsonb;  -- typed successor to metadata
```

- `event_type` is the STI discriminator, FK-checked against the registry so an
  unregistered type cannot be written once enforcement is on.
- `schema_version` records which registered payload schema the row was written
  against, so consumers can read old rows correctly after a type evolves.
- `payload` is the typed successor to `metadata`. `metadata` is **retained
  during migration** (see Export contract) and only removed once the export is
  versioned off it.
- Core columns (`actor_id`, `actor_type`, `org_id`, `resource`, `resource_id`,
  `ip_address`, `created_at`) stay promoted for indexing. `action`/`resource`
  stay populated through the transition to keep the export and `/audit` UI
  working; `action` is derivable from `event_type` and is deprecated last.

### Registry format, versioning, and validation

- **Format** — a Go catalog, one entry per event type, sibling in spirit to
  `servicePermissionVocabulary`:

  ```go
  var auditEventCatalog = []AuditEventType{
      {
          Name: "user.registered", Version: 1, Category: "identity", Owner: "accounts",
          Payload: schema.Object(map[string]schema.Field{
              "signup_method": schema.Enum("password", "sso", "magic_link"),
              "email":         schema.String().PII().Redact(),
              "org_id":        schema.UUID().Optional(),
          }),
      },
      {
          Name: "api_key.created", Version: 1, Category: "access", Owner: "accounts",
          Payload: schema.Object(map[string]schema.Field{
              "key_id": schema.UUID(),
              "scopes": schema.Array(schema.String()),
          }),
      },
      {
          Name: "auth.mfa_challenge_completed", Version: 1, Category: "security", Owner: "accounts",
          Payload: schema.Object(map[string]schema.Field{
              "transaction_id": schema.UUID(),
              "factor":         schema.Enum("totp", "webauthn"),
          }),
      },
  }
  ```

- **Projections** — a parity test seeds/verifies `audit_event_types` from the
  catalog (same discipline as the permission-vocabulary parity tests), and the
  build emits a TypeScript projection for the FE facet/renderer. The DB table is
  never hand-edited.
- **Versioning** — payload schemas are versioned per type (`version` in the
  catalog, `schema_version` stamped on each row). Evolution rule:
  backward-compatible changes (adding an optional field) bump the version and
  keep reading old rows; a breaking change registers a **new event type** rather
  than mutating an old schema, because rows are immutable and cannot be
  rewritten to a new shape. This makes "old rows stay valid forever" a property
  of the design, not a migration chore.
- **Validation — app-layer, at the single write choke point.** `Emit` already
  is the one write path; it validates `payload` against the registered schema
  for `(event_type, version)` before insert. We deliberately do **not** put
  validation in the database:
  - `CHECK` constraints can't express a per-type schema keyed on a discriminator
    without a thicket of conditional expressions, and every schema change is a
    table-rewriting migration.
  - `pg_jsonschema` is a non-core extension not guaranteed in every Postgres
    deployment a starter consumer runs; requiring it violates "keep infra
    minimal" and splits the schema definition across Go and SQL.
  Enforcement is rolled out **warn-then-fail** (log-only first, then reject) so
  a mis-registered producer surfaces before it can drop writes.

### Indexing, partitioning, search, and "math"

- **Partition `audit_events` by `RANGE (created_at)`, monthly.** This is the
  keystone: it bounds the single-hot-table con of A, keeps each index small,
  prunes time-ranged searches and rollups to a few partitions, and — critically
  — makes retention a `DROP TABLE partition` (DDL), which **sidesteps the
  append-only DELETE trigger entirely** and fixes review finding #1. Partition
  **by org** is rejected: tenant cardinality is unbounded and system events have
  NULL org.
- **Indexes** (per partition, so each stays small):
  - `(org_id, event_type, created_at DESC)` — per-tenant, per-type, time-ordered:
    the core search and the group-by driver.
  - `(org_id, actor_id, created_at DESC)` — per-user timeline.
  - `(org_id, resource, resource_id, created_at DESC)` — per-resource history.
  - `GIN (payload jsonb_path_ops)` — payload containment/search.
  - Targeted **partial/expression** indexes for a few high-value typed fields
    (e.g. `((payload->>'key_id')) WHERE event_type = 'api_key.created'`) added
    per demonstrated query, not speculatively.
  - Drop the standalone legacy `action` index once `event_type` search lands.
- **Search API** answers per user / tenant / type / resource / time from the
  composite indexes; payload predicates use the GIN index.
- **"Math" (Phase 1, default)** runs directly over the partitioned table:
  `count(*) … GROUP BY event_type`, `date_trunc('day', created_at)` time
  buckets, top-actor/top-type rollups — all index- and partition-pruned. For
  typical audit volumes this is sufficient and needs no projection.

### Immutability and RLS preservation

- **Immutability** — keep the `BEFORE UPDATE`/`BEFORE DELETE` triggers on the
  partitioned table (Postgres fires the parent's row triggers on partition
  access). Rows remain un-updatable and un-deletable. Retention no longer needs
  a row DELETE at all — it drops whole partitions — so the trigger and retention
  stop being in conflict. The now-dead row-DELETE path (`DeleteOldAuditEvents`)
  is removed as part of the rollout.
- **RLS** — the polymorphic per-`org_id` policy is defined on the partitioned
  parent and enforced on all partitions; shape is unchanged (`app.bypass` for
  control-plane, `org_id = current_setting(...)` for tenants, NULL-org visible
  only under bypass). The new `audit_event_types` table is global reference
  data: readable by all tenant roles, writable only by the control-plane /
  migration role — no `org_id`, so no per-tenant policy, and it must be excluded
  from the per-org RLS sweep the same way other global catalogs are.

### Retention and the S3 export contract

- **Retention** becomes partition lifecycle: keep the `data_retention_policies`
  row as the knob (still 365 days by default), and a scheduled job (on the
  existing job platform) drops partitions strictly older than the window. This
  makes retention O(1) DDL instead of a large DELETE, and it actually runs —
  unlike today.
- **Export contract must stay stable, so evolve it additively.** The JSONL wire
  shape (`auditExportEntry`) is a downstream contract. Rules:
  - Keep every existing top-level field, including `metadata`, for as long as
    consumers depend on it. New fields (`event_type`, `schema_version`, and a
    nested typed `payload`) are **additive**; JSONL consumers that ignore
    unknown fields keep working.
  - The one real tension: today's `metadata` is `map<string,string>`, but typed
    payloads may be nested/typed. Do **not** silently change `metadata`'s type.
    Emit the typed object under the new `payload` key and keep a flattened
    `metadata` projection during the transition; when we're ready to drop the
    flattened form, cut a **versioned export (`v2`)** rather than mutating `v1`
    in place. The export version travels in the object key prefix / a
    `schema_version` line field so a customer's downstream parser can pin.
  - Retention interacts cleanly: dropping a partition simply stops those rows
    from appearing in future export windows; already-exported objects in the
    customer bucket are theirs and untouched.

### PII and redaction (unlocked by typing)

Typed payloads make field-level classification real. Each catalog field can
carry a `PII`/`Redact` marker (see the `email` field in the example). Two
concrete wins, both enabled and neither possible with `map[string]string`:
emit-time enforcement (a field marked PII must be hashed/tokenized or the write
is rejected) and export-time redaction (the exporter drops or masks
classified fields per policy). This is called out as a first-class benefit and a
likely early follow-up ticket, not part of the core migration.

### Migration and backfill

Additive and zero-downtime; sequenced so nothing is dropped and the immutability
trigger is never fought in production.

1. **Register.** Add `audit_event_types`; seed it from the Go catalog; land the
   catalog + validator in **log-only** mode. No behavior change.
2. **Widen.** Add `event_type`, `schema_version`, `payload` as nullable/defaulted
   columns. Add the new indexes `CONCURRENTLY`. Still no behavior change.
3. **Dual-populate new writes.** `Emit` starts writing `event_type` + typed
   `payload` (and keeps writing `action`/`metadata`). Turn validation from
   log-only to enforcing once producers are clean.
4. **Backfill old rows — around the trigger.** Because `BEFORE UPDATE` blocks an
   in-place `UPDATE` (review finding #2), pick per table size:
   - **Bounded table (the starter default):** backfill inside a single migration
     that briefly suspends the trigger (`SET session_replication_role = replica`
     for the migration transaction, run as the migration owner), maps each legacy
     `(resource, action)` → a registered `event_type` (with an explicit
     `legacy.unknown` catch-all so no row is stranded), then restores it. This is
     migration-only and never exposed to the runtime roles.
   - **Very large table:** skip the in-place rewrite; read legacy rows through
     `COALESCE(event_type, map_legacy(resource, action))` and let new
     partitions carry `event_type` natively, retiring legacy partitions by
     retention over time. No trigger fight, no long lock.
5. **Partition.** Introduce range partitioning on `created_at` (new writes to
   monthly partitions; attach the existing data as the first partition or
   migrate it per table size). Move retention onto partition drop; delete the
   dead row-DELETE path.
6. **Deprecate.** Once the export is on `v2` and the UI reads `event_type`,
   deprecate and later drop `action`/`metadata`.

Backfill mapping is seeded from the ~27 known `(resource, action)` pairs; the
`legacy.unknown` type guarantees completeness for anything unmapped.

### API surface and `/audit` UI

- **Query** — extend `QueryAuditLogRequest` with `repeated event_type` and a
  small typed payload filter (`field`, `op`, `value`). Keep keyset pagination and
  the existing per-user/tenant/resource/time filters. Backward compatible:
  existing `action`/`resource` filters keep working during deprecation.
- **Aggregate** — add an `AggregateAuditLog` RPC: `group_by ∈ {event_type,
  actor, org, day/week/month}`, optional filters, returning grouped counts and
  time-bucketed series. Phase 1 runs it over the partitioned table; Phase 2
  points it at the projection (below) unchanged from the caller's view. Same
  method-policy posture as the existing read RPCs (`audit:read`,
  `SENSITIVITY_CONFIDENTIAL`, `AUDIT_EMISSION_NONE`).
- **UI** (`/admin/audit-log`) — add an event-type facet (from the generated
  registry projection), render payloads per the registered schema instead of a
  raw blob, and add a small analytics panel (events over time, top types/actors)
  backed by `AggregateAuditLog`.

### Analytics: explicit stance

**Analytics stays in Postgres. No Timescale, no external OLAP store.**

- **Phase 1 (default, ships with the redesign):** aggregate directly over the
  partitioned table using the `(org_id, event_type, created_at)` composite and
  partition pruning. This covers typical audit volumes.
- **Phase 2 (only on a measured need):** add materialized rollup tables in the
  existing `measurement` schema (e.g. per-org per-type daily counts), refreshed
  incrementally by a scheduled job on the existing job platform. This is the
  "continuous-aggregate-style" idea from Direction E realized with core Postgres
  and infra we already run. Freshness is bounded by the refresh cadence and
  surfaced in the UI ("as of …").
- **Escape hatch (not the plan):** if a concrete SLO ever proves core Postgres
  insufficient at extreme volume, Timescale continuous aggregates or an external
  OLAP sink is a documented future ADR — explicitly out of scope here and
  gated on real numbers, so we do not pay for infra we do not need.

## Consequences

- **One write path, one read path, one place to enforce invariants** are
  preserved. Immutability, RLS, retention, and export remain table-level
  concerns rather than multiplying per event type (the cost that sank C).
- **The registry is compile-time real.** Producers reference typed event
  constants, so the typo class of bug — which today silently breaks both audit
  and webhook fan-out — becomes a build error. DB and TS stay projections of the
  Go catalog, matching the existing vocabulary pattern.
- **Two latent correctness issues are fixed by the redesign, not carried into
  it:** retention actually runs (partition drop instead of a trigger-blocked
  DELETE), and backfill has a defined, trigger-aware path.
- **Search and math are first-class:** per user/tenant/type/resource/time from
  composite indexes, payload search via GIN, and aggregation over partitions —
  with a clean upgrade to in-PG rollups if volume grows, and no new datastore.
- **Typed payloads unlock PII classification and redaction** at emit and export
  time, impossible under `map[string]string`.
- **The S3 export contract survives** by evolving additively and versioning to
  `v2` only when the flattened `metadata` form is retired — never by mutating
  `v1` in place.
- **Costs accepted:** rows are wider and sparser (bounded by partitioning);
  payload validation lives in the app and must be enforced at the single write
  path (mitigated by warn-then-fail rollout and the FK-checked discriminator);
  new event types need a catalog entry + parity-test pass (cheap, and exactly
  the friction that keeps the vocabulary honest).
- **Not authorized here:** the implementation itself (separate tickets per
  migration step, the aggregation RPC, the UI work, and the PII/redaction
  follow-up), and any move to Timescale or an external OLAP store (its own
  future ADR, gated on measured need).
