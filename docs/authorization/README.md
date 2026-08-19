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
| Hierarchical / layered scope | 🟡 strawman | 🟡 seed | ◻︎ RFC-0001 draft | ◻︎ spike | ltree ancestor-match |
| Per-record sharing | 🟡 strawman | 🟡 seed | ◻︎ RFC-0002 draft | ◻︎ spike | `record_shares` overlay |
| Acting on behalf of (agents) | 🟡 strawman | 🟡 seed | ◻︎ RFC-0003 draft | ✅ chain exists | durability is the gap |
| Field-level visibility | 🟡 strawman | ◻︎ | ◻︎ | ◻︎ | likely a redaction interceptor |
| Typed scope registry | 🟡 strawman | 🟡 seed | folded into RFC-0001 | ◻︎ | closes the untyped-scope gap |
| ABAC / conditional | ◻︎ | ◻︎ | ◻︎ | ◻︎ | bounded predicates in Go |

Legend: ✅ done · 🟡 drafted/strawman (needs review) · ◻︎ not started.

## Conventions

- **RFCs** are numbered `NNNN-slug.md` from `2-proposals/_template.md`, with a
  `Status:` line (`Draft → Review → Accepted / Rejected / Superseded`).
- **ADRs** in `9-reference/decisions/` are short, dated, and **immutable once
  accepted** — supersede with a new ADR, don't edit history.
- **User stories** use `As a <persona>, I want <capability>, so that <outcome>`
  plus explicit **acceptance criteria** (the testable part).
- Everything traces back to a **persona** (`0-product/personas.md`) and respects
  the **invariants** (`1-spec/invariants.md`).

## Right now (first iteration)

The reference and a spec sketch exist; the **product layer is new and is
strawman** — start there. Read `0-product/` first, react to the personas and
stories, and only then let `1-spec/` and the RFCs firm up.
