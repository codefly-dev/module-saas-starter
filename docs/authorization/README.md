# Authorization — workstream knowledge tree

This tree is how we take authorization & authentication from **idea → shipped**.
It is **product-first**: we agree on user-facing behavior before we design
interfaces, and we design interfaces before we write migrations. The folder
numbers are the reading order.

> **Source of truth vs. views.** The committed markdown in this tree *is* the
> source of truth. The published web reference (an Artifact) and any generated
> diagrams are **presentation views** of it — regenerate them from here, never
> the other way around. The stable "how the system works today" system docs live
> at the repo root ([`AUTHZ.md`](../../AUTHZ.md), [`RLS_PLAN.md`](../../RLS_PLAN.md),
> [`AUTHZ_GAP_ANALYSIS.md`](../../AUTHZ_GAP_ANALYSIS.md)); this tree is the
> **evolving workstream** that builds on them.

## How to consume this (the gate flow)

Each stage gates the next. Work flows down; nothing skips a stage.

```
  9-reference/  ──feeds──►  0-product/  ──agree behavior──►  1-spec/
  (findings,                (user stories,                   (concepts,
   research —               personas,                        primitives,
   INPUTS, not              behaviors)                       interfaces)
   decisions)                                                    │
                                                                 ▼
                        3-implementation/  ◄──accepted RFC──  2-proposals/
                        (migrations, code,                    (RFCs: one
                         phasing, tests)                       decision each)
                                                                 │
                                                                 ▼
                                                       9-reference/decisions/
                                                       (ADRs — immutable record)
```

- **Reference** (`9-reference/`) is an **input**: the gap analysis and SOTA
  research inform product and proposals, but are never themselves the spec.
- **Product** (`0-product/`) is where a capability **starts**. If it isn't a
  user story with acceptance criteria that product + engineering both accept, it
  isn't ready to spec.
- **Spec** (`1-spec/`) turns agreed behavior into vendor-neutral **concepts,
  primitives, and interfaces** — no SQL, no library names.
- **Proposals** (`2-proposals/`) are **RFCs**: one concrete design decision per
  doc, with alternatives and a recommendation. An accepted RFC drops an **ADR**
  into `9-reference/decisions/`.
- **Implementation** (`3-implementation/`) is migrations, code sketches,
  phasing, and test plans — downstream of an accepted proposal.

## Map

| Folder | Holds | Audience |
|---|---|---|
| `0-product/` | personas, behaviors, user stories, scenarios | product + eng, together |
| `1-spec/` | concepts, invariants, primitives, interfaces | eng |
| `2-proposals/` | RFCs (numbered, status-tracked) | eng + reviewers |
| `3-implementation/` | roadmap, spikes, migrations, test plans | eng |
| `9-reference/` | current-state, SOTA research, ADRs | everyone |

## Status board

Where each capability is in the pipeline. Update this row when a stage completes.

| Capability | Product | Spec | Proposal | Impl | Notes |
|---|---|---|---|---|---|
| Hierarchical / layered scope | ✅ decided (#177) | 🟡 firm | ✅ RFC-0001 accepted → ADR-0002 | ◻︎ spike (design decided) | ltree ancestor-match; scope resolved from id |
| Per-record sharing | ✅ decided (#177) | 🟡 firm | ✅ RFC-0002 accepted → ADR-0003 | ◻︎ spike | intra-org v1; `share` is a capability |
| Acting on behalf of (agents) | ✅ decided (#177) | 🟡 firm | ✅ RFC-0003 accepted → ADR-0004 | ✅ chain exists; durability pending | chain home = Accounts; single owner |
| Field-level visibility | ✅ decided (#177) — **cut v1** | — | — | — | split RPCs by tier; B15 latent |
| Typed scope registry | ✅ decided (#177) | 🟡 firm | ✅ folded into RFC-0001 | ◻︎ | closes the untyped-scope gap |
| ABAC / conditional | ◻︎ | ◻︎ | ◻︎ | ◻︎ | bounded predicates in Go (COND questions still open) |

Legend: ✅ done/decided · 🟡 drafted (needs review) · ◻︎ not started · — n/a (cut).

## Conventions

- **RFCs** are numbered `NNNN-slug.md` from `2-proposals/_template.md`, with a
  `Status:` line (`Draft → Review → Accepted / Rejected / Superseded`).
- **ADRs** in `9-reference/decisions/` are short, dated, and **immutable once
  accepted** — supersede with a new ADR, don't edit history.
- **User stories** use `As a <persona>, I want <capability>, so that <outcome>`
  plus explicit **acceptance criteria** (the testable part).
- Everything traces back to a **persona** (`0-product/personas.md`) and respects
  the **invariants** (`1-spec/invariants.md`).

## Right now (second iteration — post #177)

The **highest-leverage product questions are resolved** (#177) and have flowed into
the spec, the three RFCs (now **Accepted**, with ADR-0002/0003/0004), and this status
board. Resolved: hierarchical scope shape + edit authority, per-record sharing reach
(intra-org v1) and who-may-share (a `share` capability), field-level (**cut v1**),
agent ownership (single human owner) + chain depth, the canonical action set (delete
& list are their own actions), nested-team inheritance (literal, no cascade), and the
load-bearing `CheckAccess` design (scope resolved from the record's id, never a caller
field). See the **[user-story backlog](0-product/stories/README.md)** for the inline
`✅ Decided` / `🔷 Deferred` markers.

**Still open:** the peripheral per-aspect questions (authentication, orgs, API keys,
service-to-service, operators, audit, conditional/time-bound, lifecycle governance)
remain `❓` for their own product review — they don't reshape the spec and aren't
blocked. Resolve them the same way: answer inline, promote into the relevant RFC.
