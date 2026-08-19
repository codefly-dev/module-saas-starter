# User stories — index

> A product backlog for **all aspects** of authentication & authorization. Each
> story is `As a <persona>, I want <capability>, so that <outcome>` + brief
> acceptance criteria + **❓ open product questions** for the team to resolve.
> Stories are **strawman** — the questions are the point; answer them and the
> spec (`../../1-spec/`) firms up.

## How to use
- Every story has a stable **ID** (`PREFIX-N`) so proposals, tests, and
  discussions can cite it.
- Personas: [`../personas.md`](../personas.md). Behavior rules (`B1…`):
  [`../behaviors.md`](../behaviors.md).
- Mark a story's questions **resolved** inline as product decides; promote the
  decision into the relevant RFC.
- **Resolution legend:** `❓` open · `🟡 **Proposed (#177 — to review)**` a call made
  in #177 with its rationale inline, **awaiting sign-off** (not yet accepted) — some
  are "propose to defer." The highest-leverage proposals also flow into
  RFC-0001/0002/0003 (Status: **Review**) and their draft ADRs
  ([`../../9-reference/decisions/`](../../9-reference/decisions/)). Nothing here is
  final until reviewed.

## Aspects, by category

The 18 aspect files group into six categories, following the mental model in
[`../../1-spec/concepts.md`](../../1-spec/concepts.md): *who you are* → *what you may
do* → *where it applies* → *on whose behalf*, plus the cross-tenant and governance
layers. Every story keeps its stable `PREFIX-N` ID regardless of category.

### 1 · Identity & tenancy — *who you are, and where*
| File | Prefix | Aspect |
|---|---|---|
| [authentication.md](authentication.md) | `AUTH` | sign-in, SSO, sessions, MFA, sign-out |
| [organizations-membership.md](organizations-membership.md) | `ORG` | orgs, invitations, membership, multi-org |
| [teams.md](teams.md) | `TEAM` | teams, nested teams, team-based grants |

### 2 · Capability — *what you may do*
| File | Prefix | Aspect |
|---|---|---|
| [roles-rbac.md](roles-rbac.md) | `ROLE` | roles, assignment, custom roles, wildcards |
| [permission-hierarchies.md](permission-hierarchies.md) | `PERM` | role inheritance, senior/junior, precedence |
| [resources-and-actions.md](resources-and-actions.md) | `RES` | resource types, read/write/delete/list, ownership |

### 3 · Scope & sharing — *where a grant applies, finer than the tenant*
| File | Prefix | Aspect |
|---|---|---|
| [hierarchical-access.md](hierarchical-access.md) | `H` | layered scope tree (grant at a level, inherit down) |
| [record-sharing.md](record-sharing.md) | `S` | per-record sharing / ACL overlay |
| [field-visibility.md](field-visibility.md) | `F` | field/column-level visibility |
| [external-and-guest.md](external-and-guest.md) | `GUEST` | external partners, guests, cross-org |

### 4 · Delegation & machine principals — *on whose behalf*
| File | Prefix | Aspect |
|---|---|---|
| [acting-on-behalf.md](acting-on-behalf.md) | `A` | delegation, agents acting for humans |
| [tools.md](tools.md) | `TOOL` | authorizing agents/integrations to use tools |
| [api-keys-programmatic.md](api-keys-programmatic.md) | `KEY` | API keys, scopes, programmatic access |
| [service-to-service.md](service-to-service.md) | `SVC` | internal calls, on-behalf-of propagation |

### 5 · Platform operations — *acting above tenants*
| File | Prefix | Aspect |
|---|---|---|
| [platform-operator-admin.md](platform-operator-admin.md) | `OPS` | cross-tenant operators, impersonation, support |

### 6 · Governance & lifecycle — *accountability over time*
| File | Prefix | Aspect |
|---|---|---|
| [audit-accountability.md](audit-accountability.md) | `AUD` | who did what, on whose behalf, review |
| [conditional-and-timebound.md](conditional-and-timebound.md) | `COND` | attributes, time-bound, break-glass, step-up |
| [lifecycle-and-revocation.md](lifecycle-and-revocation.md) | `LIFE` | offboarding, revocation, rotation, expiry |

## Cross-cutting reminder
Every story lives above the invariants ([`../../1-spec/invariants.md`](../../1-spec/invariants.md)) —
tenant isolation is absolute, default-deny, attenuation holds, fail-closed. If a
story seems to violate one, that tension is itself an open question to flag.
