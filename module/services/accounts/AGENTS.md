# AGENTS.md — accounts

Owns identity, tenancy, permissions, approvals, audit, jobs and the durable
records behind them. Authorization is the subject of [AUTHZ.md](./AUTHZ.md),
[AUTHZ_MATRIX.md](./AUTHZ_MATRIX.md) and
[../../AUTHORIZATION_CATALOG.md](../../AUTHORIZATION_CATALOG.md); every table's
scope and RLS posture is in
[../../DATABASE_AUTHORITY.md](../../DATABASE_AUTHORITY.md). This file covers the
registration and module-identity records that only accounts may write.

## The solution registry is one durable record

Both halves of a solution registration — frontend remote and gateway upstream —
are **one durable record**, not two process-local maps.
`solution_registrations` (migration `126_solution_registrations`) holds the
solution identity, its publisher, the frontend and backend halves, a
registry-wide `revision`, and a per-half lease. A registration therefore
survives a restart and reaches every replica, and is only served when it is
**whole**: a solution that registered its page but not its backend is a durable
`pending` record, deliberately absent from the navigation.

- Writes are **compare-and-swap on the revision**, so a stale publisher cannot
  overwrite newer state.
- A deregistration leaves a **tombstone**, so a retiring deployment's delayed
  heartbeat cannot resurrect it.
- accounts serves this as `SolutionRegistryService` on the **internal listener**;
  the auth-gateway is its **only** client and brokers the frontend's half,
  exactly as it brokers module registration.
- Both surfaces hold a short-lived cache rebuilt from that snapshot, so
  convergence after any write is bounded: the gateway reconciles about every 10s
  plus an on-demand refresh on a cache miss, the frontend holds a 5s snapshot TTL.

## The composed-module service principal

A module consuming the module-facing capability surface
(`ModuleCapabilitiesService`: job enqueue/claim, notify, approvals, audit,
events) calls it as its own **service principal**, whose id is derived from the
same registration prefix (`business.ModulePrincipalID`) — nothing is
hand-authored as an opaque id.

Its authority is declared in the `module-capabilities` group's
`MODULE_PRINCIPALS`, a JSON map keyed by that prefix:

| Key | Grants |
| --- | --- |
| `queues` | enqueue and claim |
| `namespaces` | event publish |
| `resources` | the permission resource types its own content is governed by, bounding both the content reads this host authorizes for it and the records it may place at a scope node |
| `tenant` | the org it is bound to |
| `cross_tenant` | an inbox worker serving every tenant |

**Unset means no module may call the surface.**

## Minting a module Work Context

The identity itself is a **Work Context**. The auth-gateway brokers the exchange
to `ModuleCapabilitiesService/MintModuleWorkContext` (`EXPOSURE_INTERNAL`), which
authorizes the presented secret against the independent
`MODULE_IDENTITY_SECRETS` digest, refuses a prefix that is not a declared module
principal, and mints a capability owned and actored by the module principal
(`aud=module-capabilities`), emitting a `module.work_context_minted` audit event
once the capability exists. The response carries
`{token, expiresAt, principalId, tenant}`.

- An **empty or absent** `MODULE_IDENTITY_SECRETS` denies every module identity
  exchange. Compositions must provision identity digests and distribute their
  matching secrets before modules can obtain Work Contexts; registration
  credentials never substitute for missing identity digests.
- The tenant is **not requestable** — it is the one `MODULE_PRINCIPALS` declares
  for that principal, so a module cannot name a tenant by asking.
- The capability **seals identity and tenant only**: what the principal may do is
  re-read from the declared grant on every call, so narrowing a grant takes
  effect immediately rather than when the outstanding token expires.
- The module presents that token in `x-codefly-work-context` on every capability
  call; accounts takes the calling principal and its bound tenant **from the
  verified token, never from request metadata**. Work Contexts cap at 15 minutes,
  so a long-running worker re-runs the exchange rather than holding one open.

See [../../WORK_CONTEXTS.md](../../WORK_CONTEXTS.md) and
[../../MODULE_INSTALLATION.md](../../MODULE_INSTALLATION.md) for the other
exchanges that produce one.

## Mesh reachability is the composition's to grant

The generated `AuthorizationPolicy` allowlists accounts' internal surface to the
service accounts of services that **declare a dependency on accounts** in the
workspace topology. This module's own topology names no composed module — it must
carry no build-time knowledge of its consumers — so a composed module reaches the
capability surface in a mesh-enforced deployment only when its own workspace
declares that dependency and regenerates the policy. A valid Work Context does
not substitute for it: mTLS refuses the call before any token is read.
