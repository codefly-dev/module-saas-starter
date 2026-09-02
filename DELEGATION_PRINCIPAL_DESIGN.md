# Per-org non-human Principal registration for delegated execution

> **Decision up front: it is CODE, and the surface already exists.** Registering
> the per-org non-human Principal that delegated Work Context flows bind against
> is `PrincipalService.CreateAgentPrincipal` — an org-admin-gated RPC in
> accounts. No new per-org data-provisioning step is needed, and none should be
> added. The Work Context mint/exchange owner-binding already resolves the actor
> against exactly this registered row.

This is a design/scoping document, not shipped code — it answers workstream **E**
of the Work Context authority-checking epic (#414) and issue #419. The mechanism
it describes is already in the tree; the only code this document lands alongside
is a regression test that pins the invariant its conclusion rests on
(`postgres_work_context_authority_test.go`,
`TestWorkContextAuthorityResolvesOnlyRegisteredAgentActor`).

All paths below were verified against the tree.

---

## 1. The question

Delegated execution on behalf of a **non-human actor** — an automated runtime
acting for a user inside an org — needs a **Principal** the org can point to,
grant scopes to, and revoke. Issue #419 asks whether *registering* that Principal
is **repo code** (an accounts / tenant-enablement surface) or **runtime data** (a
per-org provisioning action), and requires that whatever registers it is what the
mint/exchange owner-binding checks against.

## 2. What already exists

The unified identity model (`module/services/store/migrations/36_create_principals.up.sql`)
has three `kind`s in one `principals` table, so RBAC, audit, and delegation treat
every authenticatable thing uniformly:

| kind      | org scope        | how it is born                                                                 | is it a Work Context actor? |
| --------- | ---------------- | ------------------------------------------------------------------------------ | --------------------------- |
| `human`   | `org_id IS NULL` | backfilled from `users`, or at registration (cross-org)                        | no (owner, not actor)       |
| `service` | org-scoped       | **data** — backfilled from `api_keys`, one row per key (migration 37)          | **no**                      |
| `agent`   | org-scoped       | **code** — `PrincipalService.CreateAgentPrincipal` RPC, one row per install    | **yes**                     |

The "automated runtime acting for a user within an org" is the **`agent`**
principal — a per-org, org-scoped, revocable identity keyed by a canonical
`publisher/name:version` identifier. It is the only non-human kind the delegated
Work Context path accepts as an actor (§4).

The `service` kind is deliberately *not* a Work Context actor: a service
principal is derived data (one row per `api_keys` row, born and revoked with the
key's own lifecycle — `37_backfill_principals.up.sql`). A bare API-key credential
does not carry delegated Work Context authority; delegated execution flows
through a registered agent instead. Keeping these separate is the point of the
two kinds.

## 3. The registration surface (code)

Registering an agent principal is a first-class RPC, not a hand-provisioned row:

- **Wire:** `PrincipalService.CreateAgentPrincipal`
  (`module/services/accounts/proto/saas/accounts/v1/authorization.proto:672`),
  `POST /v1/principals:agent`. Its `method_policy` declares
  `EXPOSURE_AUTHENTICATED`, `TENANT_REQUIREMENT_ORG_ADMIN`, a `resource_binding`
  on `org_id → RESOURCE_TARGET_ORGANIZATION`, and audit event
  `principal.created` — so only an admin of the target org can register an agent
  in it, and every registration is audited.
- **Handler:** `PrincipalServer.CreateAgentPrincipal`
  (`pkg/adapters/principal_rpcs.go:62`) — `requireAuth` + `requireOrgAdmin`,
  then delegates to the business layer with the caller stamped as `CreatedBy`
  (the authorship root).
- **Business:** `Service.CreateAgentPrincipal`
  (`pkg/business/principals.go:269`) — **idempotent** on
  `(org_id, agent_identifier)`: re-registering the same version returns the
  existing principal (matches `codefly install` re-run semantics), while a new
  version is a *new* principal so its permissions are reviewed per version.
- **Store:** `PostgresStore.CreateAgentPrincipal`
  (`pkg/infra/postgres_principals.go:125`), under the org's RLS identity;
  uniqueness enforced by `principals_agent_identifier_org_idx` (migration 36).

### Where it is triggered in tenant enablement

Registration is an **install-time** action, not an org-creation-time one. An
agent principal comes into existence when an org admin installs/enables a
specific agent version into their org — the same moment that agent's declared
permissions are reviewed and approved. Concretely:

- **Production:** the CLI / host install path calls `CreateAgentPrincipal` (via
  `PrincipalService`) as the org admin. Org creation itself does **not**
  pre-provision agent principals — there is no fixed set of agents to seed, and
  per-version review is by design.
- **Dev / fixtures:** `fixtures/seed.go` calls
  `service.CreateAgentPrincipal(...)` for each declared fixture agent, so local
  and integration environments get the same rows through the same code path.

This is why "code vs data" resolves to **code**: the registered row is per-org
data, but it is only ever written *through the RPC/business surface* — there is
no separate provisioning step, config file, or manual `INSERT` an operator runs
per org. Adding one would be a redundant second source of truth.

## 4. The mint/exchange owner-binding already checks this Principal

The deliverable's binding requirement — *"ensure the registered Principal is what
the mint/exchange owner-binding checks against for delegated flows"* — is already
satisfied.

Every Work Context mint/exchange RPC (`StartTask`, `StartRootSession`,
`ExchangeAudience`, `StartChildSession` in `pkg/adapters/work_context_rpcs.go`)
routes actor resolution through `resolveAuthority` →
`WorkContextAuthorityStore.ResolveWorkContextAuthority`. The actor lookup
(`pkg/infra/postgres_work_context_authority.go:96`) is:

```sql
SELECT id, kind, display_name, org_id, agent_identifier, created_at, created_by
FROM principals
WHERE id = $2
  AND org_id = $1
  AND kind = 'agent'
  AND revoked_at IS NULL
```

with `pgx.ErrNoRows → "active registered agent principal required"`
(`ErrTypeNotFound`, mapped to `FailedPrecondition` at the RPC edge). So the actor
of a delegated Work Context **must** be:

1. a **registered** principal (a real `principals` row),
2. of **`kind = 'agent'`** (a `service` or unknown id fails closed),
3. in the **owner's org** (`org_id` bound to the RLS-verified tenant), and
4. **not revoked** — `RevokePrincipal` (`pkg/business/principals.go:343`) flips
   `revoked_at`, and revocation also bumps the org/principal authorization
   revision (migration 99 lineage) so already-signed contexts go stale.

The delegation-grant mint path is consistent: `DecideDelegation` /
`RequestDelegation` load the actor via `Service.GetPrincipal`
(`pkg/adapters/delegation_rpcs.go:263`, `:134`), which returns `ErrTypeNotFound`
for revoked principals — an approved grant for a revoked actor mints no usable
token.

**Net:** register through `CreateAgentPrincipal` → the same row is the actor the
owner-binding resolves, scopes are granted to it via `role_assignments`
(`subject_kind = 'principal'`), and revoking it fails every subsequent mint and
staleness-checks every outstanding context. One row, one lifecycle, no second
registry.

## 5. Decision & non-goals

**Decision:** per-org non-human Principal registration is **code** — the existing
`PrincipalService.CreateAgentPrincipal` RPC. No new enablement hook, provisioning
step, or data artifact is introduced.

**Explicitly not doing:**

- **No `CreateServicePrincipal` RPC.** Service principals stay derived from
  `api_keys`; they are not delegated-execution actors.
- **No org-creation-time agent seeding.** Registration is per-install /
  per-version so permission review stays per version.
- **No second registry / config source.** The `principals` row written by the
  RPC is the single source of truth the owner-binding reads.

**Adjacent gaps owned elsewhere in #414** (out of scope here): request-time
permission enforcement (A), fail-closed Work Context verification at callees (B),
and actor-authorized exchange past the TTL cap (C). This document only resolves
whether/where the actor Principal is registered (E) and pins that the
owner-binding checks it.
