# ADR-0005 — Field-level visibility redaction is out of scope

- **Status:** Accepted
- **Date:** 2026-08-19
- **Context:** Story [F](../../0-product/stories/field-visibility.md) asks whether
  we need a per-field redaction step — a caller who can read a record but lacks
  an extra permission sees a specific field **blanked, not errored** (behaviour
  [B15](../../0-product/behaviors.md)). The story, the gap analysis
  ([current-state](../current-state.md)), and the SOTA research
  ([sota-research](../sota-research.md)) all lean toward keeping this **minimal
  or cutting it**, and gate the work on a blunt question: *name a concrete field,
  on a concrete record, that genuinely needs F1 and isn't trivially avoidable by
  returning a narrower message.* This ADR records the answer.

## Decision
We do **not** build field-level redaction now — no `options.proto` field option,
no response-redaction interceptor. Field-level visibility stays **out of scope**
until a concrete field forces it. When one does, the mechanism sketched in the
story (a field option naming the required permission + one interceptor that
clears denied fields on the response copy, reusing `CheckPermission`) is the
intended shape; this ADR is the revisit trigger, not a rejection of that design.

## Why — the gate returns an empty list
An exhaustive pass over every read RPC in `saas.accounts.v1` (25 protos) found no
case that satisfies F1: a record two roles both legitimately read, where one
*field* of it must be gated by an **additional** permission. Every sensitive read
already resolves one of three ways, none of which needs per-field redaction:

- **Method-gated by permission.** The whole endpoint requires the permission that
  would guard the field — `QueryAuditLog`/`ExportAuditLog` (`audit:read`),
  `ListAPIKeys` (`api_keys:read`), `ListInvoices`/`OpenPortal` (`billing:read`,
  admin), `ListSubscriptions`/`ListDeliveries` (`webhooks:read`), usage
  (`entitlements:read`). Whoever reads the record already holds the permission,
  so there is nothing to split off.
- **Already a narrow message.** The broad-audience read returns a purpose-built
  type carrying only non-sensitive fields — `ListMembers`→`OrgMembership` and
  team `ListMembers`→`TeamMembership` (ids and role, **no email**),
  `InspectInvitation`→`InvitationSummary` (deliberate `email_hint`),
  `ListSubscriptions` (secret blanked), `GetOrgSettings` (branding only). This is
  the story's own preferred alternative — *split RPCs by visibility tier* — and
  it is already how the service is built.
- **Tenant-scoped by RLS.** Per [ADR-0001](0001-rls-stays-the-floor.md), reads
  run under the caller's identity (`store.As(access)`), so forced row-level
  security on `org_id` is the fail-closed floor. `GetUser` returning a full
  `User` is bounded by that floor, not open cross-org.

The concrete fields the story and [scenarios](../../0-product/scenarios.md#scenario-2--collaborating-with-an-external-auditor)
reach for — "internal annotation fields" blanked for an external auditor (F2) —
live on a *records-with-shares* domain that does not exist yet; it is the subject
of [RFC-0001](../../2-proposals/0001-hierarchical-scope-ltree.md) /
[RFC-0002](../../2-proposals/0002-per-record-sharing.md). There is no such field
to protect today.

## Consequences
- No new proto option and no redaction interceptor are added; the response path
  stays free of a marshal-time mutation step. `Sensitivity` remains a
  method-level classification (logging/tracing/docs), not a field mechanism.
- New sensitive reads must be handled the same way: gate the **method** with the
  right permission, or return a **narrower message**. Reviewers should reject a
  broad read that embeds a field needing a stricter permission — narrow the
  message instead of reaching for per-field redaction.
- When RFC-0001/0002 land a record type with genuinely mixed-visibility fields
  (e.g. internal annotations on a shared record), reopen this decision and build
  the minimal interceptor then, against a field that concretely needs it.

## What we looked at but did not treat as F1
A handful of broad reads carry a mildly-privileged field — `GetUser`
(`last_login`, `profile`), `ListUserIdentities` (`provider_email`,
`provider_data`), `GetExportStatus` (`download_url`),
`ListPendingDelegations` (`justification`). These are **method-scoping /
object-ownership** questions (should this endpoint be self-bound or admin-bound,
is the row owned by the caller), enforced by tenant requirement, resource
bindings, and RLS — not per-field redaction. If any ever needs a single field
gated by an extra permission on an otherwise-shared record, that is the trigger
above. They are not addressed here and remain governed by their method policy.
