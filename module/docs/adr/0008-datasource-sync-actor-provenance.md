# ADR 0008: A datasource's sync actor stays in the audit trail — no `last_synced_by` on the tenant row

- Status: Proposed
- Date: 2026-09-10
- Task: decides #586, the design question split out of #576 step 2. Closes the
  "who pressed sync" half of #564's ingest-provenance line; the ingest half
  (`last_ingested_at`, `last_ingested_commit`) is delivered by #576 and is
  unaffected. Authorizes no schema change and opens no follow-up work beyond
  the renderable-actor gap named under Consequences.

## Context

#564 asks `DatasourcesPanel` to render an ingest-provenance line under "Last
sync", including `last sync by <user>`. Two of the three facts that line wants
already exist on `datasource_sources` and #576 projects them. The third — the
principal that triggered a manual sync — exists nowhere on the datasource row:
`SyncDatasourceSource` receives the caller's `actorID`, records it in the audit
trail, and keeps nothing.

Three shapes were on the table: a `last_synced_by` column projected on
`Datasource`; reading the actor out of the audit trail per rendered row; or
leaving the actor to the audit surface and keeping the panel's line to ingest
facts. The choice is load-bearing because the first shape creates a second
durable record of a fact the audit spine already owns, and a second record can
disagree with the first.

### What the tree actually holds

- `datasource_sources` (migration 104, extended by 110/113/114/117) has no
  actor column of any kind.
- `saas.datasource.source.synced` is emitted on every successful
  `SyncDatasourceSource` with the caller's principal as `actor_id`
  (`pkg/business/datasources.go`), registered `CategorySystem` in the typed
  registry ([ADR 0003](0003-typed-audit-event-registry.md)).
- `buildAuditEntry` also reads the verified request identity
  (`auth.VerifiedRequestIdentity` → `Impersonated()` / `RealActorID()`, #533) and
  records `IsImpersonated` / `ImpersonatedBy` beside the actor, so the audit row
  carries the **actor chain** — the effective subject the action ran as and the
  real actor behind the request — not a single id. Both ids come from that one
  typed identity rather than a parallel metadata convention, which is precisely
  the drift #533 removed.
- `QueryAuditLog` already answers "the latest sync of this source": it filters
  `resource`, `resource_id` and `event_type`, and `postgres_audit.go` orders
  `created_at DESC, id DESC`. No new read model is required.
- `last_synced_at` is advanced **only** by `runAPISync`, `runCrawlerSync` and
  `runUploadSync` — the three leased-worker paths, for the non-GitHub
  providers. A GitHub source never advances it; GitHub ingest advances
  `last_ingested_at` / `last_ingested_commit` through
  `AdvanceDatasourceCursor`.

### The permission asymmetry

| Method | Tenant | Permissions | Response sensitivity |
| --- | --- | --- | --- |
| `DatasourceService/ListSources` | `ORG_MEMBER` | *none* | `CONFIDENTIAL` |
| `DatasourceService/SyncSource` | `ORG_ADMIN` | *none* | `INTERNAL` |
| `AuditService/QueryAuditLog` | `ORG_MEMBER` | `audit:read` | `CONFIDENTIAL` |

`ListSources` is an unpermissioned org-member read. `SyncSource` is org-admin
only. Reading who performed it requires `audit:read`.

## Decision

**The audit trail is the only record of who triggered a datasource sync.** No
`last_synced_by` column on `datasource_sources`, no actor field on the
`Datasource` message, and no server-side projection of the actor into a
datasource read. The panel's provenance line carries the ingest facts #576 put
on the wire and no actor. "Who pressed sync" is asked of the audit log, filtered
to that source.

Three reasons, each independently sufficient:

1. **It would route audit-classified data around the `audit:read` gate.**
   Projecting the actor of an `ORG_ADMIN`-only action onto `Datasource` makes it
   readable by every org member through `ListSources`, which declares no
   permission at all. The classification decision then lives in the datasource
   policy instead of the audit policy that owns it, and a tenant loses the
   ability to withhold actor provenance from ordinary members.

2. **A column flattens the actor chain.** The audit row distinguishes "user Y
   synced" from "administrator X, impersonating user Y, synced". A single
   `last_synced_by` uuid records one of those and silently drops the other —
   the record would be less correct than the one already being written.

3. **It would pair two different occurrences.** `last_synced_by` can only be
   written on the request path, in `SyncDatasourceSource`; `last_synced_at` is
   written on the worker path, after a full refetch completes, and never at all
   for a GitHub source. "Last sync by X at T" would join an actor from one event
   to a timestamp from another, and for the flagship provider the timestamp half
   stays permanently NULL while the actor half fills in.

### The line this draws, and the fields it does not touch

Other `*_by` fields on this contract stay as they are — `ScopeGrant.granted_by`,
`Principal.created_by`, `ModuleApproval.requested_by`. Those name the
**record's own** authorship: a grant without its grantor is an incomplete
authorization record, and `Principal.created_by` is the authorship root a policy
consumer chains authority back to. `last_synced_by` is not an attribute of the
datasource; it is an attribute of an *action performed on* it. Action attributes
belong to the audit spine. This ADR draws that line and does not generalize into
a rule that every actor-bearing field requires `audit:read`.

### Revisiting

Reversing this needs a superseding ADR, not a migration. The two premises to
re-examine first: whether `ListSources` has by then acquired a permission that
matches the actor's classification, and whether the fact being projected is the
same occurrence as the timestamp it renders beside. A guard test in
`pkg/cataloggen` (`datasource_sync_actor_test.go`) fails if an actor field appears
anywhere in `Datasource`'s field tree while the methods projecting it still
declare no `audit:read`, so the erosion is caught at the point it happens rather
than in review.

## Consequences

- No migration. This matters concretely: migration numbering is contested across
  several in-flight branches, and the cheapest correct answer here needs none.
- The datasources panel renders ingest provenance only. A tenant asking "who
  triggered this sync" is answered by the audit log — the surface that is
  permission-gated for it, retains the actor chain, and already supports the
  `resource_id` filter that scopes the answer to one source.
- **The renderable-actor gap moves to the audit surface, where it belongs, and
  is not closed here.** `audit-table.tsx` renders an actor as
  `truncateUUID(actorId)` and `activity-feed.tsx` as "You"/"Someone"; nothing in
  the tree resolves a principal id to a name. The path exists and is small:
  `PrincipalService/ListPrincipals` is `EXPOSURE_AUTHENTICATED` /
  `TENANT_REQUIREMENT_ORG_MEMBER`, bound to `GET /v1/principals`, and returns
  `Principal.display_name` for every principal in the org — humans via
  membership — so one call per page yields an id → name map with no per-row
  query. Filed against the audit UI as #592.
- A future per-source "recent activity" view, if one is wanted, is a read over
  `QueryAuditLog` with `resource_id` set, not a column. It inherits the
  `audit:read` gate for free.
