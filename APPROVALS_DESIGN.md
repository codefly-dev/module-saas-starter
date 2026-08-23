# General Approval primitive — `saas/approvals/v1`

> Design for a reusable "pause until an authorized decision (or timeout /
> escalation) arrives, then resume" primitive, so approval-shaped flows stop
> being bespoke, single-decider, and inconsistently audited.
>
> **Decision up front: build it Postgres + Job-backed** (generalize
> `delegation_grants`), and keep **Temporal off the critical path**. The
> decisive reason is tenant isolation — approval state must live under Postgres
> RLS, and Temporal's state lives outside it.

This is a design document, not shipped code. It resolves the engine choice and
the open questions, fixes the schema / proto / RLS / RBAC / audit surface, and
sequences the work into reviewable phases. Each phase below is a separate PR.

---

## 1. Why a primitive, and what exists today

Every approval-shaped flow in the module is hand-rolled: a request row, a
single approver gated by `requireOrgAdmin`, an ad-hoc resume, and — at best —
partial audit. There is **no quorum / dual-control / N-of-M anywhere**, no
timeout or escalation on any flow, and approver authz is coarse.

The closest existing objects, and the substrate a general primitive should
build on (all paths verified against the tree; migrations live under
`module/services/store/migrations/`, service code under
`module/services/accounts/code/`):

- **`delegation_grants`** — the nearest existing "approval" object. The approve
  RPC is `DelegationServer.DecideDelegation`
  (`pkg/adapters/delegation_rpcs.go:234`), gated only by `requireOrgAdmin`
  (`:245`) with an explicit placeholder comment to replace it with a
  per-action approve permission (`:242-245`). The table
  (`store/migrations/38_create_delegation_grants.up.sql`) already carries
  `status IN ('pending','approved','denied','expired','cancelled','active')`,
  `risk_level`, `kind`, and a `NOTIFY` trigger on status change. It **already
  has RLS** (`store/migrations/45_rls_delegation_grants.up.sql`).
- **`actor_chain_journal`** (`store/migrations/99_actor_chain_journal.up.sql`)
  — append-only + hash-chained (immutable UPDATE/DELETE triggers, `:77-93`),
  with `delegation_grant_id UUID REFERENCES delegation_grants(id)` (`:42`).
  Revocation bumps an authorization revision that invalidates live tokens.
  This is exactly the "who approved on whose behalf, immutably" substrate an
  approval primitive needs.
- **Job primitive** (`module/JOBS.md`, `store/migrations/72_...`,
  `pkg/jobs/**`, `pkg/infra/postgres_job*.go`) — a durable Postgres
  outbox/inbox worker. `EnqueueJob` **requires the caller's tx**
  (`pkg/infra/postgres_job_producer.go:53-56`, `jobs.ErrTransactionRequired`),
  so a job is committed atomically with the business mutation. Delayed jobs are
  native: `job_messages.available_at` (`72_...:59`) is respected by the claim
  query (`AND message.available_at <= NOW()`,
  `pkg/infra/postgres_jobs.go:375`). **This is our sweeper**: a delayed job with
  a future `available_at`.
- **Typed audit-event registry** (`pkg/business/audit_registry.go`, ADR
  `docs/adr/0003-typed-audit-event-registry.md`) — event-type constants
  (`:93`) and the `auditEventCatalog` master list (`:186`). Adding vocabulary =
  one const + one `def(...)` row.
- **RLS** — every per-tenant table follows the same shape
  (`store/migrations/45_rls_delegation_grants.up.sql`): `ENABLE` + `FORCE ROW
  LEVEL SECURITY`, a `<table>_tenant` policy keyed on
  `current_setting('app.current_org_id', true)` with an `app.bypass` OR-clause,
  plus verb-scoped `GRANT`s.
- **MFA tiers** — `requireMFA` (`pkg/adapters/auth.go:685`, opt-in, **fails
  open** for non-enrolled) vs `requireRecentMFA` (`:712`, strict step-up in a
  15-minute window, no enrollment escape hatch). Pick per risk tier.

### The GDPR precedent — and its one flaw we must not copy

GDPR request→completion is the flow most like what we want: an MFA-gated
request RPC (`connect_handlers.go:951-954`), a background worker that finishes
the action and emits `gdpr.deletion_completed` (`business/gdpr.go:238`). But
its resume is a **fire-and-forget goroutine** — `go s.processDeletion(...)`
(`business/gdpr.go:167`), not a durable job. A crash between the insert and
completion strands a `pending`/`processing` row with nothing to resume it.

**The approval primitive must resume through the durable jobs/outbox
(item above), not the GDPR goroutine.** GDPR shows the shape (request now,
resume the action later, emit on completion); jobs give it the durability.

---

## 2. Engine decision: Postgres + Job vs Temporal

| | Postgres + Job primitive (recommended) | Temporal |
|---|---|---|
| **Tenant isolation** | Approval state under Postgres RLS ✅ | State lives outside RLS — must re-solve tenant safety ❌ |
| Reuse | Generalizes `delegation_grants` + the Job outbox (GDPR precedent) | New stateful service + workers + datastore + k8s/Istio wiring |
| Long waits / timeout / escalation | Delayed job + sweeper (small build on `available_at`) | Native timers/signals (its strength) |
| Operational cost | ~none new | High; Temporal is doc-only today (`module/DEPLOYMENT_TOPOLOGY.md`, no service defined) |

**Recommendation:** Postgres + Job-backed, behind a small `Engine` interface
(§6) so a Temporal impl *could* be added later for genuinely long, multi-step
orchestrations — but Temporal stays off the approval critical path until its
out-of-RLS state is solved. The RLS argument is decisive: an approval row that
gates a tenant action, and the decisions on it, are tenant data and must be
isolated by the same mechanism as every other tenant table.

---

## 3. Open questions — resolved

- **Primary use case: human product approvals, agent/tool HITL, or both?**
  → **Both, one primitive, one resume mechanism (the outbox).** The data model
  and lifecycle are identical; only the resume *target* differs (a business
  mutation vs. a paused tool call). Agent HITL that needs sub-second resume
  layers a `NOTIFY`/stream on top of the same rows — exactly as
  `delegation_grants` already does with its status-change `NOTIFY` trigger for
  `WaitForDelegation`. The outbox remains the durable source of truth; the
  stream is an optimization, never the correctness path.
- **Approver model: explicit approver set vs. anyone with `approvals:decide`?**
  → **Both, expressed in `policy`.** Default is "any principal holding
  `approvals:decide` in the org." A request may additionally pin an explicit
  approver set and/or forbid the requester from being a decider (self-approval
  block). Start with the permission-only model; the explicit-set field is in
  the schema from day one but unenforced until a flow needs it.
- **Quorum policy source: per-request vs. a policy catalog?**
  → **Per-request `policy` (JSONB) now; a named policy catalog later.** N-of-M
  is a small, self-contained value; a catalog is premature until a second flow
  wants to share a policy. The `policy` column is shaped so a `policy_ref` can
  be added without migration churn.
- **Does agent HITL need sub-second resume (streaming) vs. the outbox's
  eventual resume?** → **Outbox is the contract; streaming is opt-in.** See the
  first bullet. No flow blocks on sub-second resume for correctness.

---

## 4. Data model (RLS-scoped by org)

Two tables, mirroring the `delegation_grants` + `actor_chain_journal` split
(mutable head row + append-only decision log).

```sql
-- approval_requests: the mutable head. One row per gated action.
CREATE TABLE approval_requests (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        UUID NOT NULL REFERENCES organizations(id),
    resource      TEXT NOT NULL,          -- e.g. "entitlement_override"
    action        TEXT NOT NULL,          -- e.g. "grant"
    subject       JSONB NOT NULL,         -- what is being approved (typed per resource)
    requested_by  TEXT NOT NULL,          -- actor id (matches actor-chain actor shape)
    policy        JSONB NOT NULL,         -- {quorum:N, approver_set?:[...], block_self?:bool}
    state         TEXT NOT NULL DEFAULT 'pending'
                  CHECK (state IN ('pending','approved','denied',
                                   'expired','escalated','cancelled')),
    resume_ref    JSONB NOT NULL,         -- outbox target: {job_kind, payload_ref}
    expires_at    TIMESTAMPTZ,            -- NULL = no timeout
    escalate_at   TIMESTAMPTZ,            -- NULL = no escalation
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- approval_decisions: append-only, like actor_chain_journal.
-- Quorum = count of DISTINCT approver actors with decision='approve' >= policy.quorum.
CREATE TABLE approval_decisions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id   UUID NOT NULL REFERENCES approval_requests(id),
    org_id       UUID NOT NULL REFERENCES organizations(id),  -- denormalized for RLS
    decider      TEXT NOT NULL,           -- actor id
    decision     TEXT NOT NULL CHECK (decision IN ('approve','deny')),
    reason       TEXT,
    -- links the decision to the immutable actor chain (who approved on whose behalf)
    delegation_grant_id UUID REFERENCES delegation_grants(id),
    decided_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (request_id, decider)          -- one decision per approver per request
);

-- append-only: reuse the actor_chain immutability pattern (99_...:77-93)
CREATE TRIGGER approval_decisions_no_update BEFORE UPDATE ON approval_decisions
    FOR EACH ROW EXECUTE FUNCTION actor_chain_immutable();
CREATE TRIGGER approval_decisions_no_delete BEFORE DELETE ON approval_decisions
    FOR EACH ROW EXECUTE FUNCTION actor_chain_immutable();
```

Notes:
- `UNIQUE (request_id, decider)` makes distinct-approver quorum enforceable in
  SQL and blocks double-voting without application logic.
- `approval_decisions` is append-only for the same reason the actor-chain
  journal is: a decision, once made, is evidence.
- `delegation_grant_id` on a decision is the actor-chain link — "who approved
  on whose behalf" — reusing the existing FK pattern (`99_...:42`).

### RLS (both tables)

Identical to `store/migrations/45_rls_delegation_grants.up.sql`:

```sql
ALTER TABLE approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_requests FORCE  ROW LEVEL SECURITY;
CREATE POLICY approval_requests_tenant ON approval_requests
    USING (org_id::text = current_setting('app.current_org_id', true)
           OR current_setting('app.bypass', true) = '1')
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true)
           OR current_setting('app.bypass', true) = '1');
GRANT SELECT, INSERT, UPDATE ON approval_requests TO app_tenant;
GRANT SELECT, INSERT, UPDATE ON approval_requests TO app_control_plane;
-- approval_decisions: same shape, GRANT SELECT, INSERT only (append-only).
```

The sweeper (§6) runs under the bypass role, exactly as the audit-exporter and
other cross-tenant workers do.

---

## 5. Lifecycle

```
                    ┌──────────── cancel ───────────┐
                    │                               ▼
requested ──▶ pending ──▶ approved   (quorum reached; resume via outbox)
                 │  │
                 │  ├──▶ denied      (any deny, or approver_set exhausted)
                 │  ├──▶ expired     (sweeper at expires_at)
                 │  └──▶ escalated   (sweeper at escalate_at; still decidable)
                 └─────▶ cancelled   (requester or admin withdraws)
```

- `pending → approved` fires when the Nth distinct `approve` lands. The
  transition and the outbox enqueue happen in **one transaction** (§6), so the
  gated action can never be resumed twice or lost.
- `escalated` is not terminal — escalation re-targets/notifies a wider approver
  set but the request remains decidable until `expires_at`.
- Terminal states (`approved`, `denied`, `expired`, `cancelled`) are immutable;
  a further `Decide` on a terminal request is rejected.

---

## 6. Engine interface + Postgres impl

A thin seam so Temporal can be added later without touching callers.

```go
// Engine gates an action behind a decision. The Postgres impl is the only one
// on the critical path; a Temporal impl may later back long orchestrations.
type Engine interface {
    // Create opens a request and returns its id. Enqueues the timeout/escalate
    // sweeper job in the same tx.
    Create(ctx context.Context, req ApprovalRequest) (id string, err error)
    // Decide records one approver's decision. When the Nth distinct approve
    // lands, it transitions the request to approved and enqueues the resume
    // job — atomically.
    Decide(ctx context.Context, id string, d Decision) (Outcome, error)
    Cancel(ctx context.Context, id, reason string) error
    Get(ctx context.Context, id string) (Approval, error)
    List(ctx context.Context, filter ListFilter) ([]Approval, error)
}
```

**Atomic decide + resume.** `Decide` runs inside `WithOrgTx`:
1. `INSERT` the decision (the `UNIQUE (request_id, decider)` constraint rejects
   double-votes).
2. Recount distinct approves. If `>= policy.quorum`, `UPDATE` the request to
   `approved` and `EnqueueJob(resume_ref)` — `EnqueueJob` requires this tx
   (`postgres_job_producer.go:53`), so the resume is committed with the state
   change or not at all.
3. Commit. The jobs worker later runs the resume handler, which performs the
   gated mutation and emits `approval.approved` + the domain event.

**Timeout / escalation sweeper = a delayed job, not a new cron.** On `Create`,
enqueue a job with `available_at = min(escalate_at, expires_at)`. When it runs,
it re-reads the request: if still `pending` and past `expires_at` → `expired`;
if past `escalate_at` → `escalated` + notify + re-enqueue for `expires_at`.
Idempotent: a terminal request makes the sweep a no-op. This reuses the Job
primitive's `available_at` visibility (`postgres_jobs.go:375`) — no new
scheduling infrastructure.

---

## 7. Proto — `saas/approvals/v1`

New bounded context alongside the existing ones under
`module/services/accounts/proto/saas/` (`accounts/v1`, `billing/v1`,
`jobs/v1`, `policy/v1`, …). Follow the codegen workflow in
`module/SERVICE_CATALOG.md` / `module/AUTHORIZATION_CATALOG.md` (generated Go
lands in `pkg/gen/saas/approvals/v1`, TS under the frontend `gen/` tree).

```
services/accounts/proto/saas/approvals/v1/approvals.proto
```

RPCs (Connect/gRPC, tenant-facing + internal):
`CreateApprovalRequest`, `ListApprovalRequests`, `GetApprovalRequest`,
`Decide` (approve/deny), `CancelApprovalRequest`; internal `ResumeOnApproval`
driven by the outbox worker (not a client-facing RPC).

Each RPC carries a `saas.policy.v1.method_policy` annotation, exactly like
`api_keys.proto:103-122`. `Decide` is the interesting one:

```proto
option (saas.policy.v1.method_policy) = {
  exposure: EXPOSURE_AUTHENTICATED
  tenant: TENANT_REQUIREMENT_ORG_MEMBER      // not ORG_ADMIN — permission is the gate
  permissions: "approvals:decide"
  scopes: "approvals:decide"
  resource_bindings: { request_field: "org_id" target: RESOURCE_TARGET_ORGANIZATION lookup: RESOURCE_LOOKUP_DIRECT_ID }
  mfa: MFA_REQUIREMENT_RECENT                 // high-risk tier → requireRecentMFA
  audit: { events: "approval.approved" emission: AUDIT_EMISSION_SUCCESS }
};
```

---

## 8. RBAC — `approvals:decide` as a first-class permission

Two steps, per `module/AUTHORIZATION_CATALOG.md`:
1. Add a `PermissionDefinition` (`catalog.proto:40-54`:
   `permission:"approvals:decide"`, `resource:"approvals"`, `action:"decide"`)
   to the service catalog vocabulary. The catalog compiler
   (`pkg/cataloggen/authz_methods.go`) rejects any `permissions:` value not in
   this vocabulary, so this must land before the annotations.
2. Annotate each Approvals RPC with the `permissions:`/`scopes:` value above.
   Regenerate `generated/authz-methods.json` and the edge lookup
   (`auth-sidecar/code/authz_catalog_gen.go`) via the catalog codegen.

This **retires the `requireOrgAdmin` placeholder** at
`delegation_rpcs.go:242-245`: once delegation is an approval kind (§10),
`DecideDelegation` routes through `approvals:decide` instead of the coarse
org-admin gate. Optionally, `approvals:decide` becomes per-resource later
(`approvals:decide:entitlement_override`) — the permission grammar already
supports `resource:action` scoping.

---

## 9. Audit vocabulary + actor-chain link

Add to the typed registry (`pkg/business/audit_registry.go` — one `EventType`
const near `:93`, one `def(...)` row near `:186` each):

- `approval.asked` (category: the relevant domain; STI-tagged)
- `approval.approved`
- `approval.denied`
- `approval.timeout` (emitted on `expired`)
- `approval.escalated`

Each decision links to the actor chain via `delegation_grant_id`, so "who
approved on whose behalf" is immutable and revocable through the existing
authorization-revision bump (`99_...`).

### Audit gaps to close while generalizing

The issue flags three; the tree shows two still open and one now moot:

- **`Service.CreateAgentPrincipal`** (`pkg/business/principals.go:246`) — emits
  no audit event. **Fix:** emit `principal.created`.
- **`Service.RevokePrincipal`** (`pkg/business/principals.go:318`) — emits no
  audit event. **Fix:** emit `principal.revoked`.
- **`UpsertFeatureFlag`** — *no longer a gap.* It is now a deprecated read-only
  stub that returns `FailedPrecondition "legacy feature-flag inventory is
  read-only"` (`pkg/adapters/rpcs.go:1564-1579`); it mutates nothing, so there
  is nothing to audit. No change needed.

These two principal fixes are small and independent — land them as their own
PR (they need no approval infrastructure), not bundled into the primitive.

---

## 10. Phased adoption (each phase = one PR)

1. **Schema + proto + RLS + permission.** `approval_requests` /
   `approval_decisions` migrations (with RLS + append-only triggers), the
   `saas/approvals/v1` proto, the `approvals:decide` `PermissionDefinition`,
   and the catalog regen. No behavior wired yet.
2. **Engine + Postgres impl + sweeper + outbox resume.** The `Engine` interface,
   the Postgres implementation, the delayed-job sweeper, and the outbox resume
   handler (mirroring GDPR's shape with the jobs primitive's durability).
3. **Migrate `delegation_grants` onto it.** Delegation becomes one approval
   kind; `DecideDelegation` routes through `approvals:decide`; wire the audit
   vocabulary and the actor-chain link. Retire the `requireOrgAdmin` placeholder.
4. **Adopt one real flow end-to-end with quorum** (e.g. entitlement / spend
   override): request → N-of-M decide → outbox resume → audit, `requireRecentMFA`
   on the high-risk tier.
5. **(Optional, later) pluggable Temporal engine** for long multi-step
   orchestrations — only after solving its out-of-RLS tenant state. Off the
   critical path until then.

**Independent, unblock-anything side PR:** the two principal audit fixes (§9).

---

## References

- `pkg/adapters/delegation_rpcs.go:234,242-245` (approve RPC + placeholder gate)
- `store/migrations/38_create_delegation_grants.up.sql`,
  `store/migrations/45_rls_delegation_grants.up.sql`
- `store/migrations/99_actor_chain_journal.up.sql:42,77-93` (FK + immutability)
- `store/migrations/15_platform_features.up.sql:77`, `business/gdpr.go:167,238`,
  `connect_handlers.go:951-954` (GDPR precedent — and its goroutine flaw)
- `module/JOBS.md`, `store/migrations/72_job_platform_contract.up.sql`,
  `pkg/infra/postgres_job_producer.go:53`, `pkg/infra/postgres_jobs.go:375`
- `pkg/adapters/auth.go:685` (`requireMFA`), `:712` (`requireRecentMFA`)
- `pkg/business/audit_registry.go:93,186`;
  `pkg/business/principals.go:246,318`; `pkg/adapters/rpcs.go:1564-1579`
- `module/AUTHORIZATION_CATALOG.md`,
  `proto/saas/policy/v1/options.proto`, `proto/saas/catalog/v1/catalog.proto:40-54`,
  `proto/saas/accounts/v1/api_keys.proto:103-122`
- `module/DEPLOYMENT_TOPOLOGY.md` (Temporal — doc-only, no service defined)
