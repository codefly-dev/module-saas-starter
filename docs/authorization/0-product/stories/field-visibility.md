# User stories — field-level visibility (`F`)

> **Strawman for review.** Some fields of a record are more sensitive than the
> record itself and are hidden from callers who can read the record but lack the
> extra permission. Rules: [B1, B15](../behaviors.md). Personas:
> [Member, Org Admin, External Partner](../personas.md).
>
> **Note:** the gap analysis and SOTA research both lean toward keeping this
> **minimal** — a small redaction step, not a framework, out of scope for most
> fields. These stories exist to decide *whether* we need even the minimal version.

> ✅ **Decided (#179): CUT field-level visibility from v1 — see
> [ADR-0005](../../9-reference/decisions/0005-field-level-visibility-out-of-scope.md).**
> #177 proposed the cut; ADR-0005 accepts it. The blunt scoping test below asks
> for one real field that needs "can read the record but not this field." In the
> current accounts domain there is none: the sensitive cases (billing/payment
> detail on `subscriptions`, MFA state, operator-only metadata) are already gated
> by **separate RPCs / roles**, not by reading a record and hoping to blank a
> column. So we do **not** build the redaction interceptor; we **split RPCs by
> visibility tier** (a narrower message for the lower tier). B15 stays in the
> behavior catalog as a *latent* rule — if a concrete field ever genuinely needs
> it, this reopens as the minimal redaction step (roadmap P2, "if a real field
> needs it"), not a per-field policy language.

### F1 · Hide a sensitive field from a role that can read the record
**As an** Org Admin, **I want** a specific field (billing detail, personal identifier, internal note) hidden from members who can otherwise read the record, **so that** "can read the record" doesn't imply "can read everything on it."
- Acceptance: a member with record read sees the protected field **blanked**, not an error — the response still succeeds (B15); a caller with the extra permission sees the real value; the client can distinguish "blanked because denied" from "genuinely empty."
- ✅ **Resolved — no real F1 field (see [ADR-0005](../../9-reference/decisions/0005-field-level-visibility-out-of-scope.md)).** An exhaustive pass over every `saas.accounts.v1` read RPC found none: sensitive reads are already method-gated by permission, return a narrower message, or are RLS-scoped. Cut for v1; if a field ever appears it is a fixed proto-annotated set, never per-tenant dynamic.

### F2 · External partners never see internal fields
**As an** Org Admin, **I want** internal-only fields never to appear for shared external partners, **so that** sharing a record doesn't leak internal annotations.
- Acceptance: a partner reaching a record via a share sees only the non-internal fields (B1, B15).
- ✅ **Resolved — deferred to the sharing domain (see [ADR-0005](../../9-reference/decisions/0005-field-level-visibility-out-of-scope.md)).** Not required for guests either: guests are deferred (no cross-org sharing in v1) and, when they land, get a **narrower projection message**, not field redaction ([`external-and-guest.md`](external-and-guest.md) GUEST-4). The "internal annotation fields" F2 blanks would live on the records-with-shares domain from [RFC-0001](../../2-proposals/0001-hierarchical-scope-ltree.md) / [RFC-0002](../../2-proposals/0002-per-record-sharing.md); there is no such field today. Revisit when that domain lands a record type with genuinely internal-only fields.

### The blunt scoping decision this set must resolve — ✅ RESOLVED
Build the minimal redaction step **only if at least one real field genuinely needs F1**; otherwise declare field-level **out of scope** and split RPCs by visibility tier.
> Name the concrete fields, on which records, that need F1. If the list is empty
> or trivially avoidable by returning a narrower message, we do **not** build this.

**Outcome: the list is empty / trivially avoidable → do NOT build this.** Split
RPCs by visibility tier instead. See
[ADR-0005](../../9-reference/decisions/0005-field-level-visibility-out-of-scope.md):
the service already splits by tier (method-level permission gates, narrow
messages, RLS floor), so no redaction interceptor is built now. The field option +
interceptor stays the intended shape for when a concrete field forces it (e.g.
internal annotations once RFC-0001/0002 land record sharing).

### Explicitly not in scope
- Field-level *encryption* / masking vs. DB-operator access — a compliance/threat-model concern (regulated PII), tracked separately, not role-based field visibility.
- A general per-field policy language.
