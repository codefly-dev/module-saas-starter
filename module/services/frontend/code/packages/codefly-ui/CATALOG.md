# The component catalog

Status: **proposal** (issue #451). The agreed list of components `@codefly-dev/ui`
should ship — the shared vocabulary every solution `Page.tsx` and every module UI
composes from. This document is the sibling of [ARCHITECTURE.md](./ARCHITECTURE.md)
(the layer stack and invariants) and [TOKENS.md](./TOKENS.md) (the token contract):
those two say *how* a kit component is built and themed; this one says *which*
components exist and *where each one lives*.

## Why the catalog exists

Layers are **sealed** — a component ships once, from the layer that owns it, and a
higher layer composes it rather than re-implementing or overriding it (invariant 5
in [ARCHITECTURE.md](./ARCHITECTURE.md), the mechanism landed in #450). Today that
is only half-true. The kit ships the *composites* (layout / dashboard / chat), but
the *primitives* still live in the host app's `src/components/ui` and are not
exported from the kit, so a module that needs a `Badge` or a `Table` re-inlines its
own copy. The kit's `src/__tests__/no-reinlined-primitives.test.ts` guard exists
precisely because that has already happened. A re-inlined primitive forks the
styling: a skin or spacing change propagates to the kit's copy and silently skips
the fork.

The catalog closes the gap by naming one home for every component. Promoting the
primitives into the kit makes the seal real instead of aspirational: with the
primitive exported from the kit there is a single instance to compose, so there is
nothing to re-inline.

## How to read the tables

Each row is one component with an **owner tier** (from the layer stack in
[ARCHITECTURE.md](./ARCHITECTURE.md)) and a **status**:

- **ships** — exported from the kit today.
- **promote** — written in the host `src/components/ui`, to move into the kit as
  the single sealed instance (the host then imports it back).
- **propose** — net-new, no home yet.

The **v1** column marks the minimum subset the current solutions actually need
(see [v1 subset](#v1-subset)); everything else is v2+ and lands as its own build
issue once v1 proves the pattern.

## 1. Ships today (composites)

Exported from the kit now. Listed for completeness; no work.

| Component | Owner tier | Subpath |
| --- | --- | --- |
| `Card`, `Section`, `Tabs` | layout | `@codefly-dev/ui/layout` |
| `Dashboard` + chart atoms (`AreaChart`, `BarList`, `LineChart`, `StatChart`, `Axis`, `Gridline`, `Svg`) + scales/format + `DashboardData` | dashboard / charts | `@codefly-dev/ui/dashboard` |
| `Chat` (+ `ChatMessage`) | chat | `@codefly-dev/ui/chat` |
| skin resolver / view-descriptor precedence | skin | `@codefly-dev/ui/skin` |
| plugin host | plugin-host | `@codefly-dev/ui/plugin-host` |

## 2. Promote into the kit (primitives that exist in the host, not yet shared)

These are the shadcn-lineage primitives already written in
`module/services/frontend/code/src/components/ui`, built on Base UI + `cva` + `cn`
(the [house conventions](#house-conventions) below). They should ship **from the
kit** as the single sealed instance so modules stop re-inlining them. Each maps
one-to-one to an existing host file, so promotion is a move (kit becomes source of
truth), not a rewrite — see [decision 1](#d1-primitive-ownership).

All land at the **layout** tier unless noted. `Sidebar` is shell/nav and sits
alongside — see [decision 5](#d5-sidebar-vs-tabs).

| Group | Component | Host source | v1 |
| --- | --- | --- | --- |
| Actions | `Button` | `components/ui/button.tsx` | ✓ |
| Forms | `Input` | `components/ui/input.tsx` | ✓ |
| Forms | `Textarea` | `components/ui/textarea.tsx` | ✓ |
| Forms | `Label` | `components/ui/label.tsx` | ✓ |
| Forms | `Checkbox` | `components/ui/checkbox.tsx` | ✓ |
| Forms | `Switch` | `components/ui/switch.tsx` | ✓ |
| Forms | `Select` | `components/ui/select.tsx` | ✓ |
| Forms | `InputGroup` | `components/ui/input-group.tsx` | |
| Data display | `Badge` | `components/ui/badge.tsx` | ✓ |
| Data display | `Avatar` | `components/ui/avatar.tsx` | ✓ |
| Data display | `Table` | `components/ui/table.tsx` | ✓ |
| Data display | `Skeleton` | `components/ui/skeleton.tsx` | ✓ |
| Data display | `Separator` | `components/ui/separator.tsx` | ✓ |
| Overlays | `Dialog` | `components/ui/dialog.tsx` | ✓ |
| Overlays | `AlertDialog` | `components/ui/alert-dialog.tsx` | ✓ |
| Overlays | `Sheet` | `components/ui/sheet.tsx` | |
| Overlays | `Tooltip` | `components/ui/tooltip.tsx` | ✓ |
| Overlays | `DropdownMenu` | `components/ui/dropdown-menu.tsx` | ✓ |
| Overlays | `Command` | `components/ui/command.tsx` | |
| Feedback | `Sonner` (toast) | `components/ui/sonner.tsx` | ✓ |
| Shell / nav | `Sidebar` | `components/ui/sidebar.tsx` | |

Every one of these carries Tailwind/shadcn classes, so promotion is unblocked only
because the CSS-ownership question is now settled — see
[decision 2](#d2-css-ownership).

## 3. Propose (net-new — standard SaaS surface we don't have yet)

No home today. Grouped by tier. The shared **feedback/state** components are the
highest-value net-new work: `<DataTable>` (host `shared/ui/data-table.tsx`) and
other surfaces already hand-roll empty/loading/error states inline, so lifting them
to first-class kit components de-duplicates real code rather than speculating.

| Group | Component | Owner tier | Notes | v1 |
| --- | --- | --- | --- | --- |
| Feedback / state | `EmptyState` | layout | Lift the inline empties in `DataTable` / list surfaces | ✓ |
| Feedback / state | `ErrorState` | layout | Pairs with `EmptyState` | ✓ |
| Feedback / state | `Spinner` / inline loading | layout | Complements `Skeleton` for indeterminate waits | ✓ |
| Feedback / state | `Banner` / `Callout` | layout | Page-level and inline advisory | |
| Feedback / state | `Progress` | layout | Determinate progress | |
| Data | `DataTable` | table | Sortable/filterable/paginated wrapper over `Table`; host `shared/ui/data-table.tsx` is the seed | ✓ |
| Data | `Pagination` | layout | Standalone; also consumed by `DataTable` | ✓ |
| Data | `DescriptionList` | layout | Key/value pairs | |
| Data | `StatTile` / metric | layout | Reconcile with dashboard `StatChart` and host `shared/ui/metric-tiles.tsx` | |
| Data | `Tag` / `Chip` | layout | Reconcile with `Badge` (interactive vs. static) | |
| Data | `Timeline` | layout | | |
| Forms | `Form` (field + validation shape) | form | The field/validation contract every form composes | ✓ |
| Forms | `RadioGroup` | layout | | ✓ |
| Forms | `Combobox` | layout | Composes `Command` + popover | |
| Forms | `DatePicker` | layout | | |
| Forms | `FileUpload` | layout | | |
| Navigation | `Breadcrumbs` | layout | | ✓ |
| Navigation | `PageHeader` | layout | Title + actions + breadcrumb slot | ✓ |

## Decisions

The issue's four open questions. #448 (CSS) and #450 (sealing) have since landed,
so the two questions the catalog was *gated* on now have answers rather than
options.

### D1. Primitive ownership

**The kit is the single source of truth. Primitives move *out* of the host app
into the kit; the host imports them back.** Not duplicated.

Sealing (#450, invariant 5) requires one home per component — a solution renders
against the one shared instance a layer publishes, and cannot shadow it. A
duplicated primitive (one copy in the host, one in the kit) is two instances, which
is exactly the fork the seal exists to prevent, and exactly what
`no-reinlined-primitives.test.ts` already fails on. So each promoted primitive lands
in the kit at its owner tier and the host `src/components/ui/<x>.tsx` becomes a
re-export of the kit's `<x>` (or is deleted and its importers repointed). The host
keeps zero primitive *definitions*.

### D2. CSS ownership

**Settled by #448 / core-solutions#48: the kit ships its own compiled CSS; the host
owns only token *values* (the skin).** A remote/module renders styled standalone,
without a host-provided global stylesheet. That is what unblocks this catalog —
every component here renders with Tailwind/shadcn classes, and until the kit emitted
its own stylesheet a promoted primitive would have rendered unstyled in a remote.
With #448 landed, promotion no longer waits on anything.

### D3. v1 scope

**v1 is the subset the current solutions (lastlogin, wiki) actually need**, marked
`✓` in the tables and collected under [v1 subset](#v1-subset). Ship value before the
full catalog. Everything unmarked is v2+.

### D4. API conventions

One house style, applied across the catalog (see [house conventions](#house-conventions)).
Note this diverges from the issue's shorthand: the primitives are built on **Base UI**,
so composition is Base UI's `render` prop, **not** Radix's `asChild`.

### D5. `Sidebar` vs. `Tabs`

`Sidebar` (shell/nav) and the composite `Tabs` (layout) are different tiers and stay
separate components; there is nothing to merge. The reconcile note in the issue is
resolved as: `Tabs` is in-page section switching, `Sidebar` is app-shell navigation.

## v1 subset

The minimum to make the seal real for the current solutions and to stop the
re-inlining the guard is catching. Two buckets:

**Promote (already written — move into the kit):**
`Button`, `Input`, `Textarea`, `Label`, `Checkbox`, `Switch`, `Select`, `Badge`,
`Avatar`, `Table`, `Skeleton`, `Separator`, `Dialog`, `AlertDialog`, `Tooltip`,
`DropdownMenu`, `Sonner`.

**Propose (net-new — small, high-leverage):**
`EmptyState`, `ErrorState`, `Spinner`, `DataTable`, `Pagination`, `Form`,
`RadioGroup`, `Breadcrumbs`, `PageHeader`.

Rationale: the promote bucket is a move, not a rewrite, so it is cheap and it
directly retires the re-inlining. The net-new bucket is the shared state/data/nav
surface that `DataTable` and the feature pages already hand-roll — lifting it
de-duplicates existing code. `Sheet`, `Command`, `InputGroup`, `Sidebar`, and the
richer data/forms components (`Combobox`, `DatePicker`, `FileUpload`, `Timeline`,
`DescriptionList`, `StatTile`, `Tag`, `Banner`, `Progress`) are v2+.

## House conventions

Every catalog component follows the pattern the promoted primitives already use, so
the promote work is a move and net-new components match on sight:

- **Primitive base** — build on `@base-ui/react` where a headless primitive exists
  (focus, keyboard, ARIA). Composition uses Base UI's `render` prop, not `asChild`.
- **Variants** — `variant` / `size` via `class-variance-authority` (`cva`), with a
  `defaultVariants`. Export the `*Variants` function alongside the component
  (e.g. `buttonVariants`) so composites can reuse the class set.
- **`className` passthrough** — every component accepts `className`, merged last via
  `cn(...)` so a caller can extend without fighting specificity.
- **`data-slot`** — each rendered element carries a `data-slot="<name>"` attribute
  for styling hooks and testing.
- **Controlled / uncontrolled** — follow Base UI: accept both a controlled `value` /
  `open` prop and an uncontrolled `defaultValue` / `defaultOpen`.
- **Tokens only** — color and spacing come from the token vocabulary in
  [TOKENS.md](./TOKENS.md); no hard-coded color. This is what lets a skin swap
  re-theme the whole catalog.
- **Pure, data-in** — components fetch nothing and read no host context (invariant 3).

## Next steps — per-component build issues

With the list, the v1 subset, and the ownership + CSS answers agreed, the catalog is
ready to fan out into build issues:

1. **Promote v1 primitives** — one move issue per component (or one batched issue for
   the set), each: add the definition to the kit at its owner tier, add a subpath /
   export, repoint the host `src/components/ui/<x>.tsx` to re-export the kit, extend
   `no-reinlined-primitives.test.ts` to guard the newly-owned class strings.
2. **Net-new v1 components** — one build issue each for `EmptyState`, `ErrorState`,
   `Spinner`, `DataTable`, `Pagination`, `Form`, `RadioGroup`, `Breadcrumbs`,
   `PageHeader`, seeding `DataTable` from `shared/ui/data-table.tsx`.
3. **v2+** — file the remaining rows as they become needed, not speculatively.
