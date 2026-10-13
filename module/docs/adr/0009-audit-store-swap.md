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
   added to this module, and the only migration is the one that lets a user-scoped
   transaction publish an entry with no organization (item 2a). The type is
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
   Migration 36 replaces the function with an identical body except that the
   `app_tenant` branch also admits one case: the tenant is NULL, the type is
   `saas.audit.recorded.v1`, `app.current_user_id` is set and non-empty, and the
   event's actor (`p_actor_principal_id`) is that same user. It mirrors the rule
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
   `ModuleCapabilitiesService` and starts one task per event. That task calls a
   Runnable operation on this module, which reads the journal row by event id,
   takes the organization from the row and never from the caller, and appends the
   entry through `AuditStorage` to the warehouse. An entry with no organization
   runs in the installation's tenant. Delivery is at-least-once and keyed by
   event id; every read returns each entry once by id. The read RPC and the
   Runnable operation are specified separately and are not part of this ADR's
   first change.

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

## Options considered

| Option | Verdict |
| --- | --- |
| Keep the tee only | Rejected for the long-retention deployment: Postgres still grows and neither copy is complete. Kept as is for the deployments that need only a copy. |
| A dedicated audit outbox table, relay and archive in this module (the earlier draft) | Rejected: a fourth module-local outbox beside three that exist, a migration, a relay that must call the delivery runtime with an identity this module would have to mint. The journal already has every property the outbox was built for. |
| Write to the warehouse on the request path | Rejected: an external system cannot join the transaction, so a state change could commit without its record. |

## Consequences

- No state change commits without its journal event, and no entry is lost between
  the commit and the warehouse as long as the journal row is retained until
  delivered. `domain_events` has no enforced retention today (EVENTS.md names 30
  days; nothing enforces it), so until delivery exists and is monitored, the
  journal row is the safeguard and an undelivered entry is recoverable from it.
- A deployment cannot select `warehouse` until the warehouse `AuditStorage` and
  the delivery path exist in the build; startup refuses it by name until then.
- Every no-organization entry is published on the transaction the caller already
  holds, including a user-scoped one: `publish_domain_event` admits a tenant-less
  `saas.audit.recorded.v1` from `app_tenant` when its actor is the transaction's
  user (migration 36). A tenant-less event of any other type, or for any other
  actor, is still refused from `app_tenant`. The comparison is on the text of the
  actor id and of `app.current_user_id`, which are the canonical lowercase user id
  everywhere they are set; a non-canonical spelling of the same id is refused where
  `audit_events` would accept it, which fails closed.
