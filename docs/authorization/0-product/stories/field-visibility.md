# User stories — field-level visibility

> **Strawman for review.** Capability: some fields of a record are more sensitive
> than the record itself and are hidden from callers who can see the record but
> lack the extra permission. Rules cited: [B15, B1](../behaviors.md). Personas:
> [Member, Org Admin, External Partner](../personas.md).
>
> **Note:** the gap analysis and SOTA research both lean toward keeping this
> **minimal** — a small redaction step, not a framework, and out of scope
> entirely for most fields. These stories exist to decide *whether* we need even
> the minimal version.

## Story F1 — Hide a sensitive field from a role that can read the record

**As an** Org Admin, **I want** a specific field (e.g. billing detail, personal
identifier, internal note) hidden from members who can otherwise read the record,
**so that** "can read the record" doesn't imply "can read everything on it."

**Acceptance criteria**
- A member with record read access sees the record with the protected field
  **blanked**, not an error (B15) — the response still succeeds.
- A caller with the extra permission sees the real value.
- The client can tell "blanked because denied" from "genuinely empty."

## Story F2 — External partners never see internal fields

**As an** Org Admin, **I want** internal-only fields to never appear for shared
external partners, **so that** sharing a record doesn't leak internal annotations.

**Acceptance criteria**
- A partner reaching a record via a share sees only the non-internal fields
  (B1, B15).

## Scoping decision this story set must resolve
The recommendation from research is: **build the minimal redaction step only if
at least one real field genuinely needs F1**, and otherwise declare field-level
**out of scope** and split RPCs by visibility tier instead. So the product
question is blunt:

> Name the concrete fields, on which records, that need F1. If the list is
> empty or trivially avoidable by returning a narrower message, we do **not**
> build this.

## Explicitly not in scope
- Field-level *encryption* / masking against DB-operator access — that's a
  compliance/threat-model concern (regulated PII), tracked separately, not
  role-based field visibility.
- A general per-field policy language.

## Open questions
- Is there a real F1 field in the first product domain, or is this hypothetical?
- If yes, is it a fixed small set (annotate in proto) or dynamic per tenant?
