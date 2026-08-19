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

## Aspects

| File | Prefix | Aspect |
|---|---|---|
| [authentication.md](authentication.md) | `AUTH` | sign-in, SSO, sessions, MFA, sign-out |
| [organizations-membership.md](organizations-membership.md) | `ORG` | orgs, invitations, membership, multi-org |
| [roles-rbac.md](roles-rbac.md) | `ROLE` | roles, assignment, custom roles, wildcards |
| [permission-hierarchies.md](permission-hierarchies.md) | `PERM` | role inheritance, senior/junior, precedence |
| [resources-and-actions.md](resources-and-actions.md) | `RES` | resource types, read/write/delete/list, ownership |
| [tools.md](tools.md) | `TOOL` | authorizing agents/integrations to use tools |
| [teams.md](teams.md) | `TEAM` | teams, nested teams, team-based grants |
| [hierarchical-access.md](hierarchical-access.md) | `H` | layered scope tree (grant at a level, inherit down) |
| [record-sharing.md](record-sharing.md) | `S` | per-record sharing / ACL overlay |
| [acting-on-behalf.md](acting-on-behalf.md) | `A` | delegation, agents acting for humans |
| [field-visibility.md](field-visibility.md) | `F` | field/column-level visibility |
| [api-keys-programmatic.md](api-keys-programmatic.md) | `KEY` | API keys, scopes, programmatic access |
| [service-to-service.md](service-to-service.md) | `SVC` | internal calls, on-behalf-of propagation |
| [platform-operator-admin.md](platform-operator-admin.md) | `OPS` | cross-tenant operators, impersonation, support |
| [audit-accountability.md](audit-accountability.md) | `AUD` | who did what, on whose behalf, review |
| [conditional-and-timebound.md](conditional-and-timebound.md) | `COND` | attributes, time-bound, break-glass, step-up |
| [lifecycle-and-revocation.md](lifecycle-and-revocation.md) | `LIFE` | offboarding, revocation, rotation, expiry |
| [external-and-guest.md](external-and-guest.md) | `GUEST` | external partners, guests, cross-org |

## Cross-cutting reminder
Every story lives above the invariants ([`../../1-spec/invariants.md`](../../1-spec/invariants.md)) —
tenant isolation is absolute, default-deny, attenuation holds, fail-closed. If a
story seems to violate one, that tension is itself an open question to flag.
