# Concepts & glossary

> Vendor-neutral vocabulary. No SQL, no library names — those live in
> `3-implementation/` and the RFCs. This is the shared language the product
> stories and the proposals both use.

## The mental model

Authorization answers three questions, in order:

1. **Who are you?** — authentication. Normalizes every caller (human SSO, API
   key, service) into one internal **identity**.
2. **What may you do?** — three layers: handler gates → RBAC capability → row
   security. See [`AUTHZ.md`](../../../AUTHZ.md).
3. **On whose behalf?** — delegation: agents/services acting for a human/user,
   bounded by attenuation.

## Terms

- **Principal** — a subject that can hold authority. Three kinds: **human**,
  **service**, **agent**. Humans mirror 1:1 to users; agents and services are
  first-class non-humans owned/authorized by a human.
- **Team** — a group subject; a human inherits its teams' grants.
- **Tenant / Org** — the isolation boundary. Every record belongs to exactly one.
- **Role** — a named bundle of `(resource, action)` permissions.
- **Scope node** — a node in a tenant's typed hierarchy (e.g. a solution, a
  customer). Identified by a **scope path** (materialized path from the root).
- **Scope grant** — "principal (or team) holds role **at** a scope node,"
  inheriting to the node's subtree.
- **Record share** — "subject may act on **this specific record**," across the
  ownership boundary; the per-instance overlay on top of hierarchy.
- **Record-scope binding** — the stored mapping `resource_id → scope path`.
  `CheckAccess` resolves a record's scope from this binding, never from a caller
  field, so a path can't be forged (the same discipline that binds id→org for RLS).
- **Action set** — the typed vocabulary `read · list · create · write · delete ·
  share · export · admin`; `admin ⊇ write ⊇ read` implies down that ladder, the rest
  are orthogonal (granted explicitly).
- **Capability (Work Context)** — a short-lived signed token carrying an
  **actor chain**: who is acting on whose behalf, each hop **attenuating**.
- **Attenuation** — the rule that a delegation hop can only *narrow* authority,
  never widen. Enforced at both sign and verify.
- **RLS floor** — the physical, always-on tenant filter in the database. The
  invariant everything else sits above.
- **PDP** — policy decision point; the component that answers "is this allowed."
  Today it's `CheckPermission`; the hierarchical/record answer is `CheckAccess`.
- **Effective role** — the role a caller actually has on a record after combining
  scope grants and shares (highest-grant-wins), at the most-specific scope.

## How the terms compose

```
identity ──► capability (actor chain, on-behalf-of, attenuated)
                │
                ▼
   PDP decision on a record:
     tenant floor (RLS)  ⟶  must pass, always
     capability (role → resource:action)  ⟶  CheckPermission (flat / org-wide)
     scope grant (role @ ancestor scope node)  ⟶  CheckAccess (hierarchy)
     record share (role @ this record)         ⟶  CheckAccess (overlay)
     field permission (extra permission per field) ⟶ deferred (cut from v1, #177)
```

## See also
- Primitives, one per file: [`primitives/`](primitives/)
- The non-negotiables: [`invariants.md`](invariants.md)
- The interface surface: [`interfaces.md`](interfaces.md)
