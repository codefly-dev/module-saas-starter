# User stories — field-level visibility (`F`)

> **Strawman for review.** Some fields of a record are more sensitive than the
> record itself and are hidden from callers who can read the record but lack the
> extra permission. Rules: [B1, B15](../behaviors.md). Personas:
> [Member, Org Admin, External Partner](../personas.md).
>
> **Note:** the gap analysis and SOTA research both lean toward keeping this
> **minimal** — a small redaction step, not a framework, out of scope for most
> fields. These stories exist to decide *whether* we need even the minimal version.

### F1 · Hide a sensitive field from a role that can read the record
**As an** Org Admin, **I want** a specific field (billing detail, personal identifier, internal note) hidden from members who can otherwise read the record, **so that** "can read the record" doesn't imply "can read everything on it."
- Acceptance: a member with record read sees the protected field **blanked**, not an error — the response still succeeds (B15); a caller with the extra permission sees the real value; the client can distinguish "blanked because denied" from "genuinely empty."
- ❓ Is there a real F1 field in the first product domain, or is this hypothetical? Fixed small set (annotate in proto) or dynamic per tenant?

### F2 · External partners never see internal fields
**As an** Org Admin, **I want** internal-only fields never to appear for shared external partners, **so that** sharing a record doesn't leak internal annotations.
- Acceptance: a partner reaching a record via a share sees only the non-internal fields (B1, B15).
- ❓ Which fields are "internal"? Is field-hiding required specifically for guests? (see [`external-and-guest.md`](external-and-guest.md))

### The blunt scoping decision this set must resolve
Build the minimal redaction step **only if at least one real field genuinely needs F1**; otherwise declare field-level **out of scope** and split RPCs by visibility tier.
> Name the concrete fields, on which records, that need F1. If the list is empty
> or trivially avoidable by returning a narrower message, we do **not** build this.

### Explicitly not in scope
- Field-level *encryption* / masking vs. DB-operator access — a compliance/threat-model concern (regulated PII), tracked separately, not role-based field visibility.
- A general per-field policy language.
