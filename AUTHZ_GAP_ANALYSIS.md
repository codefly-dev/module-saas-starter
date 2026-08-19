# Authorization gap analysis — layered / structured / hierarchical data access control

> Companion to [`AUTHZ.md`](AUTHZ.md). That document describes the three
> layers the starter **ships**. This one asks a narrower question the
> shipped stack does not yet answer: *can the authorization stack express
> **layered / structured / hierarchical data access control**, and where are
> the gaps?*
>
> This is a **review + gap analysis, not an implementation**. Its output is a
> grounded confirm/refute of each suspected gap, a gap matrix, an explicit
> **in-scope-primitive vs. installed-product-concern** decision per gap, and
> spikes for the two things ruled in-scope. No behavioural code changes ship
> with it.

## The three isolation questions

Authorization in this starter answers three separate questions. `AUTHZ.md`
frames the first two; this analysis is about the third.

1. **Is the caller in this tenant?** — physical isolation. Answered rigorously
   by L3 RLS (`org_id` equality, forced, fail-closed) plus L1 `requireOrgMember`.
2. **Does the caller hold this capability?** — RBAC. Answered rigorously by L2
   `CheckPermission` (`resource:action` with wildcards + team inheritance) and
   L1 `requireScope` for API keys.
3. **Which records, sub-resources, and fields *within* a tenant may this
   *specific* caller touch — for data that is hierarchical or nested?** — the
   axis this analysis cares about. This is where the gaps are.

Questions 1 and 2 are strong. Question 3 is largely unanswered for anything
beyond flat, whole-row, org-owned data.

## Method

Every finding below is grounded in the current tree. The load-bearing citations:

- L1 gates + scope matching: `module/services/accounts/code/pkg/adapters/auth.go`
- L2 RBAC decision: `module/services/accounts/code/pkg/infra/postgres_permissions.go:320`
  (`CheckPermission`)
- `role_assignments.scope` semantics: `AUTHZ.md` "Scope semantics" + the query at
  `postgres_permissions.go:369-374`; the untyped column at
  `4_create_roles_permissions.up.sql:28` and `93_role_catalog_columns.up.sql:4-7`
- MethodPolicy + resource bindings:
  `module/services/accounts/proto/saas/policy/v1/options.proto`,
  validated in `module/services/accounts/code/pkg/business/rpc_policy.go`
- `delegation_grants`: `module/services/store/migrations/38_create_delegation_grants.up.sql`,
  `module/services/accounts/code/pkg/business/delegation_grants.go`,
  `.../pkg/adapters/delegation_rpcs.go`
- Team tree: `teams.path` / `parent_team_id` in
  `module/services/accounts/code/pkg/gen/saas/accounts/v1/common.pb.go:793-810`,
  `.../pkg/infra/postgres_claims.go:126-154`
- RLS policy shapes: every `CREATE POLICY` in `module/services/store/migrations`

---

## Findings — confirm / refute, per suspected gap

### Gap 1 — No hierarchical / nested-resource scope — **CONFIRMED (with a partial substrate)**

`role_assignments.scope` is a flat opaque string compared with **strict
equality**. `CheckPermission` builds the scope predicate as:

```sql
-- postgres_permissions.go:369-374
AND (ra.scope IS NULL OR ra.scope = $N)   -- scoped check
AND ra.scope IS NULL                       -- unscoped check
```

There is no `LIKE`/prefix match, no path grammar, no parent→child precedence,
and no "most-specific-visible-wins" resolution. A NULL scope is org-wide and
subsumes all scopes; a scoped grant never widens (`AUTHZ.md` "Scope semantics").
So `analyst on foundation/solution-x` cannot, today, imply visibility of
`foundation/solution-x/customer-y` — the strings are simply unequal.

**Partial substrate worth naming.** The starter *does* ship one genuine
hierarchy: **teams form a strict tree** with `parent_team_id` and a materialized
slug-path (`teams.path`, e.g. `engineering/platform`), unique per org, maintained
on write (`common.pb.go:793-810`). But authorization over that tree is
deliberately **literal, not subtree-expanded**: `CheckPermission`'s team
inheritance resolves `ra.subject_id IN (SELECT team_id FROM team_members WHERE
user_id = $1)` (`postgres_permissions.go:341-343`) — membership you literally
hold — and `ListTeamPathsForUser` is documented "Literal membership — consumers
that want subtree semantics expand ancestors themselves"
(`postgres_claims.go:126-128`). So the materialized-path substrate exists, but
no PDP walks it. A hierarchical scope resolver would be building *on* an existing
pattern, not from zero.

### Gap 2 — No per-record / cross-owner sharing ACL beyond ownership — **CONFIRMED**

RLS + resource bindings prove *row-belongs-to-caller's-org* (see Gap-matrix
"per-record ownership"). There is **no primitive for "grant user X or team Y
access to record 123" across the ownership boundary.**

The obvious candidate — `delegation_grants` — is **not** that primitive. It is a
**just-in-time (JIT) escalation / break-glass** mechanism, not a durable ACL:

- On approval it **mints a short-lived bearer token**
  (`ScopedAuthorization`, `TTL: 5*time.Minute, MaxUses: 1`) that the actor then
  presents to the target — `delegation_rpcs.go:341-395` (`mintApprovedToken`),
  streamed back via `WaitForDelegation` (`:210-219`).
- The PDP **never joins `delegation_grants`.** `CheckPermission` reads only
  `role_assignments → roles → role_permissions` (`postgres_permissions.go:351-376`);
  grepping the permission path for "delegation" returns nothing.
- Grants are **time-bounded** (`expires_at NOT NULL`, migration `38…:63`) and
  use-capped (`max_uses` / `use_count`), never standing.
- `resource_id` exists on the row (`38…:38`) but is **inert** — the minted token
  is scoped by `Action`/`Resource` only (`delegation_rpcs.go:373-374`),
  `MatchPattern` globs `action`/`resource` never `resource_id`
  (`postgres_delegation_grants.go:475-485`), and no SQL predicate filters on it.
  It is justification/audit context, not an enforced record-level scope.
- Its own RLS policy is plain `org_id` equality
  (`45_rls_delegation_grants.up.sql:7-15`) — a grant row is visible to anyone
  acting in the org, which is the opposite of a per-user record ACL.

So `delegation_grants` answers "let an agent briefly borrow authority with a
human in the loop," not "user A durably shared record 123 with user B."

### Gap 3 — No field / column-level authorization or redaction — **CONFIRMED**

Authorization is whole-row (RLS) and whole-method (policy). Nothing masks
individual columns by a *caller's* role or permission.

- `request_sensitivity` / `response_sensitivity` are **method-level** enums that
  control "log body capture, trace attributes, generated examples, and
  support-tool visibility" (`options.proto:114-136`, `METHOD_POLICY.md:49-51`) —
  observability capture, **not** per-field access. There is no field-level
  sensitivity option on message fields.
- The redaction helpers in the tree are uniform, not caller-conditioned:
  `RedactPayload` strips PII by event type for audit/webhook export regardless of
  role (`audit_registry.go:375-399`); `maskEmail` produces a fixed hint for
  everyone (`invitations.go:270`); `redactPrivilegedRPCs` filters whole RPC
  entries out of the introspection catalog by tier (`introspection.go:253-258`) —
  method-list filtering, not record-field redaction. `FieldMask` usages are
  partial-update request shaping, not authz.
- `organization_member_primary_email` (SECURITY DEFINER,
  `69_user_directory_operations.up.sql:12-35`) returns a single column to a
  co-member — but that is a deliberately narrow *query/operation*, not a
  per-field access-control layer over a record.

Notably, the production reference model cited in the issue also stops at
whole-asset visibility with **no** field/section-level authz — evidence that #3
is genuinely hard/rare, not an accidental omission.

### Gap 4 — No ABAC / conditional predicates — **CONFIRMED**

Scope is string-equality; RLS is `org_id`/`user_id` equality. **No** authz path
evaluates a row attribute (record `status`, `classification`, ownership-team,
time-bound window) as an access condition.

- Every `CREATE POLICY` `USING`/`WITH CHECK` across the migrations reduces to
  `org_id`/`user_id`/`current_setting` equality (plus the bypass escape), or a
  membership subquery (still identity equality) — e.g.
  `33_rls_organizations`, `30_rls_teams`, `28_rls_api_keys`,
  `42_rls_principals`, `35_rls_sessions`. The only attribute predicate in the
  authz neighbourhood — `u.status <> 'deleted'` — lives inside a SECURITY DEFINER
  helper (`69…:25`) as data hygiene, not a caller-attribute condition.
- `CheckPermission` evaluates resource/action (exact or `*`), org (equality/NULL),
  and scope (equality/NULL) only (`postgres_permissions.go:357-374`). No status,
  classification, time, or ownership-attribute predicate anywhere.

This is consistent with the starter's stated philosophy: `MethodPolicy` "is not a
general policy language … state-dependent rules … remain in tested business code"
(`METHOD_POLICY.md:9-12`).

### Gap 5 — Nested/structured document data has no sub-resource authz — **CONFIRMED**

RLS operates per row. JSONB columns exist (`org_settings` attributes, api-key
`scopes`, `request_context`, journey attributes) but none is authorized at the
sub-key level — the JSONB helpers are value operations (`settings_jsonb_deep_merge`,
`settings_jsonb_delete_path`), not access control. There is no subtree/projection
authorization primitive and no scope-path grammar (`{scope}/{owner}/{solution}/{tail}`)
anywhere. A hierarchical document stored as JSON cannot be authorized at the
sub-part level without decomposing it into RLS-governed rows.

### Gap 6 — Scope taxonomy is untyped — **CONFIRMED**

`role_assignments.scope` is free-form: a nullable `TEXT` with no FK, enum, or
CHECK — `4_create_roles_permissions.up.sql:28` (`scope TEXT, -- optional
fine-grained scope e.g. "projects/foo"`), part of a uniqueness key only. No
table, enum, or constant declares a set of valid scopes or a hierarchy among
them; a search for a scope registry / allowed-scope / scope-precedence construct
finds nothing. The catalog importer's only scope "validation" is a character-set
regex (`rolecatalog/catalog.go` `namePattern`), not a value set. It records
`roles.scope` but that column is explicitly "recorded but not consulted by the
assignment path" — `93_role_catalog_columns.up.sql:4-7` ("Assignment-time
derivation lands with strict scope semantics", future tense; `AssignRole` never
reads `roles.scope`). Nothing validates a scope string on write or resolves
relationships between scopes. Note the migration-4 example value `"projects/foo"`
already *looks* like a path — but `CheckPermission` compares the whole string
with `=` (`postgres_permissions.go:370`); the `/` is never split or resolved. This is the enabling gap under #1: without
a typed registry the compiler/PDP has nothing to validate or resolve against.

---

## Gap matrix

Rows are the granularity axes; columns are support level and the layer that
enforces (or would enforce) each. **Supported** = a first-class, enforced
primitive. **Partial** = structural substrate exists but is not resolved by any
PDP, or is enforced only by convention. **Absent** = no primitive.

| Axis | Status | Enforcing layer | Evidence |
|---|---|---|---|
| **Tenant isolation** | ✅ Supported | L3 RLS (`org_id` equality, forced, fail-closed) + L1 `requireOrgMember` | `AUTHZ.md`; every `CREATE POLICY` |
| **Capability (RBAC `resource:action`)** | ✅ Supported | L2 `CheckPermission` (wildcards + team inheritance); L1 `requireScope` (API keys) | `postgres_permissions.go:320`; `auth.go:546` |
| **Per-record ownership** (row ∈ caller's org/owner) | ✅ Supported | L2 resource bindings (`RESOURCE_TO_ORGANIZATION` / `RESOURCE_TO_OWNER`) + L3 RLS | `options.proto:26-42`; `rpc_policy.go:272-280` |
| **Cross-owner sharing** (share record with another subject) | ❌ Absent | — (`delegation_grants` is JIT token elevation, not an ACL) | `delegation_rpcs.go:341-395`; `38…:38` |
| **Nested / hierarchical scope** (parent→child, precedence) | ❌ Absent | — for scope. **Partial** substrate: team tree (`teams.path`) resolved literally, not subtree | `postgres_permissions.go:369`; `postgres_claims.go:126-128`; `common.pb.go:793-810` |
| **Typed scope taxonomy / registry** | ❌ Absent | — (free-form string, no validation) | `AUTHZ.md` "Scope semantics"; no registry found |
| **Field / column-level** | ❌ Absent | — (sensitivity governs logging, not access) | `options.proto:114-136`; `METHOD_POLICY.md:49-51` |
| **ABAC / conditional predicates** | ❌ Absent | — (equality only; predicates live in domain business code) | `postgres_permissions.go:357-374`; `METHOD_POLICY.md:9-12` |
| **Sub-resource / subtree (nested docs)** | ❌ Absent | — (per-row RLS only) | JSONB helpers are value ops, not authz |

---

## In-scope primitive vs. installed-product concern

The dividing principle, taken from the starter's own philosophy
(`METHOD_POLICY.md:9-12`, `DATABASE_AUTHORITY.md:9`): the **starter ships
mechanism and enforcement substrate that every product needs and that must be
correct at the DB/PDP boundary**; the **product owns state-dependent domain
policy**. Concretely — the starter should own a primitive when (a) it belongs at
the tenant/DB isolation boundary where a bug leaks data, and (b) essentially
every product re-implements it. It should stay product-owned when it encodes
domain state or shape that the starter cannot know.

| Gap | Decision | Rationale |
|---|---|---|
| **1 — hierarchical scope + precedence** | **In-scope primitive (spike).** The starter owns a *typed hierarchical scope* dimension + a precedence-resolving PDP primitive; the product declares its own scope tree. | `scope` is already a first-class L2 column and a token claim (`sr`). Making it typed + hierarchical with a resolver is generic mechanism; the *shape* of the tree is product data. Draw the line there. |
| **2 — per-record / cross-owner grant** | **In-scope primitive (spike).** A generic `resource_grants` table + an RLS-composable predicate + a `CheckAccess` primitive. | "Share this record with that subject" is universal and lives exactly at the row-isolation boundary the starter already owns. Absent it, every product invents an ad-hoc, RLS-bypassing sharing table — the highest-risk place to improvise. |
| **6 — typed scope registry** | **In-scope**, folded into spike (1). | Precedence resolution is meaningless without a validated registry to resolve against. Same primitive. |
| **3 — field / column-level** | **Out of scope. Declared a product concern.** | Field visibility is domain projection: the product knows which columns are sensitive to whom. The cited production reference model also stops at whole-asset. Starter stance: authorize the row/method; project fields in tested business code. |
| **4 — general ABAC** | **Out of scope as an engine. Product concern.** | A general predicate language contradicts "`MethodPolicy` is not a general policy language." Attribute/state conditions stay in domain code. The **one** attribute pattern common enough to promote — instance sharing — is captured by spike (2), not by a general ABAC engine. |
| **5 — nested-document subtree authz** | **Out of scope. "Everything authorization-relevant is a row."** | Sub-part authz over opaque JSON has no safe generic enforcement at the DB boundary. Products that need it decompose the subtree into RLS-governed rows (which spikes 1 + 2 then cover). |

Net: **two things ruled in-scope** — (a) typed hierarchical scope + precedence
resolution, and (b) a per-record grant/ACL primitive — matching the issue's
prediction. Three ruled explicitly out (field-level, general ABAC, JSON-subtree),
each with a stated reason so the line is not silently drawn.

---

## Spikes for the in-scope items

These are **design sketches to de-risk**, not committed implementations. Each is
sized to land as its own PR behind its own tests. Open questions are called out
rather than papered over.

### Spike A — Typed hierarchical scope + precedence resolution

**Goal.** Let a grant `analyst @ foundation/solution-x` satisfy a check at
`foundation/solution-x/customer-y` (a descendant), and let the resolver, when the
same logical entitlement is captured at several scopes, return the single
highest-precedence scope the caller may see.

**Where it lives.** L2 (RBAC), reusing `role_assignments.scope`. RLS (L3) stays
`org_id` equality — hierarchy is a *within-tenant* capability question, and L3's
job is physical tenant isolation, not fine-grained scope. (Revisit only if a
product needs the DB itself to enforce sub-org partitions.)

**Sketch.**
1. **Typed registry.** A `scopes` relation per org: `(scope_path text, parent_path
   text NULL, kind text)`, materialized-path like `teams.path` already is. Writes
   validate that `parent_path` exists → this closes Gap 6.
2. **Storage.** `role_assignments.scope` continues to hold a `scope_path`. A CHECK
   (or FK) ties it to the registry, so an unknown scope fails on write instead of
   silently never matching.
3. **Resolution.** Change the `CheckPermission` scope predicate from
   `ra.scope = $N` to "`ra.scope` is an **ancestor-or-equal** of the requested
   `scope_path`" — i.e. the requested path is under the granted path. With
   materialized paths this is a prefix test (`$requested LIKE ra.scope || '/%'
   OR ra.scope = $requested`), guarded so `foo` cannot prefix-match `foobar`.
   NULL scope stays org-wide.
4. **Precedence.** For "return the most specific asset the caller may see," the
   resolver orders candidate scopes by path depth and returns the deepest visible
   one — a metadata-visibility filter applied *before* content loads (the
   default-deny visibility filter the reference model describes).

**Open questions.**
- Does precedence resolution (pick-the-most-specific-visible asset) belong in the
  starter PDP, or is it product query logic that merely *consumes* the starter's
  ancestor-match check? Leaning: starter ships **ancestor-match** (a pure
  capability question); **precedence/visibility projection** stays product-side.
- The `sr` token claim (`X-Scoped-Roles`) is exact-match today
  (`HasScopedRole`, `auth.go:510`). Hierarchical matching on the hot path means
  either expanding ancestors into the claim (size pressure against the 64-pair
  bound) or always resolving via Path B `CheckPermission`. Needs a decision.
- Team subtree (`teams.path`) and scope hierarchy are two materialized-path trees.
  Do they unify, or stay separate concepts that happen to share a shape?

### Spike B — Per-record grant / ACL primitive

**Goal.** A first-class, RLS-composable "subject S may do action A on record R"
grant that survives across the ownership boundary — the durable ACL
`delegation_grants` deliberately is not.

**Sketch.**
1. **Table.** `resource_grants (id, org_id, resource_type text, resource_id text,
   subject_id, subject_kind, action text, granted_by, created_at, expires_at NULL)`.
   Tenant-scoped, forced RLS on `org_id` (same shape as every other relation).
2. **Decision primitive.** `CheckAccess(subject, resource_type, resource_id,
   action)` — a new oracle beside `CheckPermission`, returning allow if the
   subject (or a team it belongs to) has a matching, unexpired grant. Team
   inheritance reuses the existing `team_members` subquery.
3. **Enforcement composition.** Two options to weigh:
   - *Handler-side*: business code calls `CheckAccess` where a record may be
     shared (simplest; consistent with resource-binding style).
   - *RLS-side*: a policy that also admits rows the caller has a `resource_grants`
     row for — makes sharing physical, but couples every shared table's policy to
     the grants table and needs care against the "grant row visible to whole org"
     footgun (scope grant *visibility* per subject).
4. **Audit.** Reuse the typed audit registry — `resource_grant.created` /
   `.revoked` events.

**Open questions.**
- Handler-side vs. RLS-side enforcement — the former is simpler and localized; the
  latter is defense-in-depth but invasive. Likely start handler-side, with the
  RLS predicate as a documented follow-up per shared table.
- `resource_type` is a free string here — does it want the same typed registry as
  Spike A (a resource-kind taxonomy)? Probably, eventually; keep them alignable.
- Interaction with hierarchical scope (Spike A): is a per-record grant just the
  depth-0 special case of a scope grant, or a genuinely separate primitive? They
  can ship independently; a later unification is possible but not required.

---

## Summary

- All six suspected gaps are **confirmed** against the code. Two carry an
  important nuance: `delegation_grants` is JIT token elevation (not a per-record
  ACL), and the team tree is a real materialized-path hierarchy that the PDP
  nonetheless resolves **literally**, not by subtree.
- The starter is rigorous on **tenant** and **capability** isolation and on
  **whole-row ownership**; it is **absent** on cross-owner sharing, hierarchical
  scope, typed scope taxonomy, field-level authz, ABAC, and nested-document
  subtree authz.
- **In-scope as starter primitives:** typed hierarchical scope + precedence
  (Spike A) and a per-record grant/ACL primitive (Spike B). **Out of scope as
  starter primitives (product-owned):** field-level authz, a general ABAC engine,
  and JSON-subtree authz — each declared out with a reason.
- Next step is to schedule Spikes A and B as their own work items; this document
  is the decision record they build from.
