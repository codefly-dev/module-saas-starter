# Current state — what's true today

> **Input, not decision.** This summarizes what the system does *now*, so product
> and proposals build on fact. The canonical, detailed docs live at the repo root
> and are the authority; this is the index + one-paragraph orientation.

## Canonical system docs (root)
- [`AUTHZ.md`](../../../AUTHZ.md) — the three-layer model (L1 handler gates, L2
  RBAC, L3 forced RLS), scope semantics, worker bypass, role catalog.
- [`RLS_PLAN.md`](../../../RLS_PLAN.md) — the RLS rollout, table coverage, test
  pattern.
- [`AUTHZ_GAP_ANALYSIS.md`](../../../AUTHZ_GAP_ANALYSIS.md) — the confirmed gaps
  for layered/hierarchical data (the review that started this workstream).
- [`module/DATABASE_AUTHORITY.md`](../../../module/DATABASE_AUTHORITY.md) — DB
  roles, grants, the fail-closed migration rule.

## One-paragraph orientation
Authentication normalizes every caller (WorkOS/OIDC humans, API keys, internal
services) into one Ed25519-JWT identity behind an auth-sidecar that stamps trusted
identity headers the api still re-verifies. Authorization is three orthogonal
layers: handler gates, RBAC (`CheckPermission`: `resource:action` + wildcards +
team inheritance + a **flat** scope string), and forced Postgres RLS
(`org_id` equality, fail-closed). Principals are first-class (human/service/agent);
agents are human-owned. A signed **Work Context** capability carries a multi-hop
**attenuating** actor chain, and `delegation_grants` provides single-hop
human-approves-agent elevation.

## What's strong (don't rebuild)
- Tenant isolation (RLS floor, fail-closed).
- Capability RBAC (single-sourced `role_permissions`).
- The attenuating capability chain (dual-enforced, revision-gated).

## The confirmed gaps (drive the proposals)
| Gap | RFC that addresses it |
|---|---|
| No hierarchical/nested scope | RFC-0001 |
| No per-record cross-owner sharing | RFC-0002 |
| Untyped scope taxonomy | RFC-0001 |
| Chain not durable / linked / auditable | RFC-0003 |
| No field-level authz | product decision pending ([story F](../0-product/stories/field-visibility.md)) |
| No ABAC / conditional | not yet scoped (bounded predicates in Go — see research) |
| No nested-doc subtree authz | covered by RFC-0001 (decompose to rows) |

Full evidence and file:line citations: the root gap analysis and the published
reference artifact.
