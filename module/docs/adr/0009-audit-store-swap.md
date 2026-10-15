# ADR 0009: Audit storage swap — a warehouse as the audit store of record, reached through the journal

- Status: Proposed
- Date: 2026-10-12
- Supersedes, in part: [ADR 0006](./0006-audit-sink-and-retention-tiers.md) — its
  sink decision, "a tee, not a swap" (Decision item 2; option C of its Options
  considered). A deployment that selects the warehouse backend replaces
  `audit_events` as its store of record instead of copying from it. ADR 0006's
  admission policy and sizing stand, and the tee (`AUDIT_SINK=both`) is unchanged
  for every deployment that does not select the warehouse.
- Refines: [ADR 0003](./0003-typed-audit-event-registry.md) — "analytics stays in
  Postgres" now holds for the `postgres` backend only. The registry and the single
  write path are unchanged.
- This ADR replaces an earlier draft of the same number that put a dedicated
  outbox table, a relay and an archive inside this module.

## Context

ADR 0006 kept Postgres as the only store of record and added an opt-in tee. That
is the right answer for a deployment that wants a copy elsewhere. It does not
serve one whose audit must be kept for years and queried heavily: the tee grows
Postgres instead of shrinking it, its copy lacks the entries with no organization
and the ones the authentication resolver records, and every reader has to know
which copy answers which question.

The requirement ADR 0006 refused to trade away still stands: no state change
commits without its record, and no external system can join a Postgres
transaction.

This module already has a transactional record that is not `audit_events`: the
journal, `domain_events`, written in the producer's transaction through the event
transport ([EVENTS.md](../../EVENTS.md)). It is durable, replayable and carries a
tenant, and an `internal` type in it is never fanned out.

## Decision

1. **A swap, not a tee.** `audit-storage` is a workspace group with
   `AUDIT_STORAGE_BACKEND = postgres | warehouse`, plus the warehouse dataset and
   table names and nothing else: no endpoint, project, credential or token. The
   backend is chosen once, at startup, by `AuditService`, over one interface,
   `AuditStorage` (append, list, aggregate). `postgres` is the default and is
   today's behaviour exactly: the entry is written to `audit_events` in the
   caller's transaction, and `VerifyAuditWiring` holds. Under `warehouse`,
   Postgres keeps no audit history, and `AUDIT_SINK=both` is refused at startup:
   the tee copies `audit_events` rows and there would be none.

2. **The journal, not an outbox.** Under `warehouse`, `AuditService` publishes
   every audit entry, including one with no organization, to the journal as the
   internal type `saas.audit.recorded.v1`, carrying the whole entry, in the
   caller's transaction through the existing transport. No table, relay or drain is
   added to this module. Migration 41 admits user-scoped publication (item 2a),
   and migration 42 records the writing transaction for the journal cursor. The type is
   code-owned and `internal`: the
   relay marks it published with no fan-out, a replay skips it, and no
   subscription may match it. The organization-scoped webhook event of an external
   audit type is published beside it, unchanged. The journal event's `data` is
   limited to 960 KiB ([EVENTS.md](../../EVENTS.md)); an entry that does not fit
   is refused and fails the caller's transaction, because until it is delivered
   the journal event is the entry's only copy.

   2a. **A user-scoped transaction publishes its own no-organization entries.**
   Some entries with no organization are written inside a user-scoped transaction
   (the WebAuthn registration and the MFA enrolment, for example). That
   transaction runs as `app_tenant` and sets only `app.current_user_id`, and
   `publish_domain_event` refused every event with no tenant from `app_tenant`.
   Migration 41 replaces the function with an identical body except that the
   `app_tenant` branch also admits one case: the tenant is NULL, the type is
   `saas.audit.recorded.v1`, `app.current_user_id` is set and non-empty, and the
   event's actor (`p_actor_principal_id`) is that same user. The record in `data`
   must name that actor and no organization as well; an envelope cannot disguise
   a record that the original audit row policy would refuse. It mirrors the rule
   the `audit_events_tenant` policy already applies to the same entry's row
   (`org_id IS NULL` and `actor_id` equal to `app.current_user_id`), so the journal
   admits exactly what `audit_events` admits and nothing wider; every other
   refusal, the advisory lock and the insert are unchanged. The emitter puts the
   entry's actor id on the envelope's `actor_principal_id`, which for a person is
   their user id (a human principal's id is the `users.uuid` that
   `sync_human_principal` copies). The entry is published on the transaction the
   caller already holds; nothing moves the writer to the control plane. The type
   is internal, so the relay never fans the row out.

3. **module-runtime reads the journal.** This module never calls module-runtime
   and mints no identity of its own. module-runtime, which holds a `cross_tenant`
   grant, reads the journal through a cross-tenant read RPC on
   `ModuleCapabilitiesService` (`ReadJournal`) and hands this module one range of
   it at a time. The read serves rows in `(xact_id, seq)` order and only below the
   oldest transaction still running (migration 42; [EVENTS.md](../../EVENTS.md)), so
   a cursor never passes a row that can still commit; its positions are opaque,
   and `"start"` is the position before the oldest retained entry. It marks the
   entries that are the reader's own delivery footprint (`delivery_machinery`), so
   the reader never parses this module's payloads, and so delivering the audit
   trail does not feed itself. The reader then calls a Runnable operation on this
   module, `DeliverAuditEvents{from, to}`, for the range `(from, to]`. The
   operation refuses a range whose end is not yet visible or that holds more than
   500 audit records, reads the range's records from the journal, takes each
   record's organization from its row and never from the caller, and appends each
   entry through `AuditStorage` to the warehouse. It emits no audit of its own.
   Its receipt is one per range, effect id `saas.audit.recorded.v1/<from>`, in
   the installation's tenant. Both invocation and lookup take the same `{from, to}`
   body. The host derives its range receipt key internally; the runtime's
   transport effect id identifies the task operation separately. Lookup verifies
   the same request digest and reports a missing receipt as `NOT_FOUND`; a
   successful lookup is always proof of a committed range. An entry with no
   organization needs no tenant of its own. Delivery is at-least-once, and overlapping ranges append an entry
   twice; every read returns each entry once by id.
   Delivery and lookup require a verified, idempotent operation context addressed
   to `saas-audit`, a live cross-tenant module grant, and `saas.audit-delivery`
   with `invoke` or `read`, respectively. Human membership and API-key scopes do
   not grant this cross-tenant operation. Authorization is repeated after the
   receipt serialization hold, before either execution or replay.
   A binding id is marked as delivery machinery only for the calling module;
   another module reusing that id remains ordinary traffic. Journal pages fit
   the cumulative JSON byte bound and a 3 MiB projected protobuf bound, with
   the cursor ending at the last entry actually returned. The producer also
   checks the encoded size of each audit record before committing it, so a
   compact numeric array cannot leave an unreadable record in the journal.

   *Revised 2026-10-13.* The first draft started one task per event and read the
   journal row by event id. Delivering per event made each delivery's own audit (the
   mint of its Work Context, its exchanges) an event to deliver, and so on without
   end. Page delivery holds machinery-only pages until their oldest entry is
   older than one hour, then delivers them too. An idle deployment therefore
   settles at one delivery per hold instead of one per polling pass. The swap,
   journal type and remaining decisions stand.

4. **Reads come from the warehouse.** The activity list, aggregation and export go
   through `AuditStorage`. A warehouse read names its scope (an organization, or
   explicitly the platform), because row-level security does not reach it. The
   warehouse refuses the percentile metric, which it cannot compute exactly over a
   deduplicated, at-least-once store; the service answers it as an invalid
   argument. The grant check of a resource-gated aggregate still runs in the
   organization's Postgres transaction, but the read that follows is a second step
   with no shared snapshot. Reads lag the write by the time delivery takes, and a
   page that shows an "as of" is honest about it.

5. **Retention is the warehouse's.** `RunRetention` skips the `audit_events` policy
   under `warehouse` and logs why; how long an entry is kept is the warehouse
   table's own expiry. Rows written before the switch stay in `audit_events`.
   Under `warehouse`, the same retention run removes journal entries of exactly
   `saas.audit.recorded.v1` whose `created_at` is older than thirty 24-hour days
   on the database clock and whose position lies in a committed delivery's
   `(from, to]` interval. The committed response stores both positions; retention
   verifies the response status, range effect identity and original request
   digest before trusting an interval. Overlapping or touching intervals may be
   combined, but gaps remain unproven. Legacy receipts without both positions,
   malformed receipts and mismatched digests preserve the journal rows; the
   `delivered` count never supplies missing coverage. Every warehouse retention
   run logs `deleted`, `old_unproven` and `retention_days`, making old entries
   without delivery proof visible. PostgreSQL mode and other journal types are
   unchanged.

   Source receipt cleanup names only the two datasource operation methods, so
   the full audit delivery receipt, including its range, survives cleanup.
   Migration 43 gives the existing control-plane role SELECT only on audit
   delivery receipts and restricts its new journal DELETE grant by event type
   and a database-enforced 720-hour minimum age. No receipt mutation, table,
   relay, queue or identity is added.

## Options considered

| Option | Verdict |
| --- | --- |
| Keep the tee only | Rejected for the long-retention deployment: Postgres still grows and neither copy is complete. Kept as is for the deployments that need only a copy. |
| A dedicated audit outbox table, relay and archive in this module (the earlier draft) | Rejected: a fourth module-local outbox beside three that exist, a migration, a relay that must call the delivery runtime with an identity this module would have to mint. The journal already has every property the outbox was built for. |
| Write to the warehouse on the request path | Rejected: an external system cannot join the transaction, so a state change could commit without its record. |

## Consequences

- No state change commits without its journal event, and no entry is lost between
  the commit and the warehouse as long as the journal row is retained until
  delivered. Undelivered or otherwise unproven audit journal entries survive
  indefinitely. Delivered audit entries expire from the journal only after
  thirty days in warehouse mode; retention for the other `domain_events` types
  remains unenforced.
- A deployment cannot select `warehouse` until the warehouse `AuditStorage` and
  the delivery path exist in the build; startup refuses it by name until then.
- Every no-organization entry is published on the transaction the caller already
  holds, including a user-scoped one: `publish_domain_event` admits a tenant-less
  `saas.audit.recorded.v1` from `app_tenant` when its actor is the transaction's
  user (migration 41). A tenant-less event of any other type, or for any other
  actor, is still refused from `app_tenant`. The comparison is on the text of the
  actor id and of `app.current_user_id`, which are the canonical lowercase user id
  everywhere they are set; a non-canonical spelling of the same id is refused where
  `audit_events` would accept it, which fails closed.

## Build or reuse

| Component | Searched and found | Decision |
| --- | --- | --- |
| Audit storage and journal delivery | This repository's existing journal, closed #1000's storage interfaces and parity suite, and the shared `core`, `sdk-go`, and `saas-sdk-go` implementations; existing receipts and module capability verification are implemented and tested. | Reuse the journal, storage shapes, SDK receipt guard, and live module authority checks. Extend the existing adapters; no parallel queue, identity provider, or receipt implementation. |
| Audit journal retention | Read `RunRetention`, journal position SQL, `PruneSourceOperationReceipts`, SDK `receipts` protobuf/digest storage and PostgreSQL receipt tests; searched shared `core`/`sdk-go` implementations and used Go's standard sorting. Source cleanup already preserves audit receipts, but the old committed response omitted the interval end. | Extend the existing committed response with `from`/`to` and reuse its effect identity, request digest, receipt table and control-plane transaction. Preserve legacy coverage as unproven; add only the bounded role permissions needed to retire delivered rows. |
