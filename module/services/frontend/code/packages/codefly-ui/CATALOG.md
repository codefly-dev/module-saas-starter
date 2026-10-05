# Component catalog

The merged implementation is tracked by the host's
[UI inventory](../../ui-inventory.json) and
[migration decisions](../../UI_MIGRATION.md). The earlier catalog proposal was
not evidence that all primitives, stories or production callers had migrated.

## Public presentation tiers

| Tier | Contract |
| --- | --- |
| `layout` (rank 1) | Native and Base UI controls, compound CardRoot/TabsRoot, data-in Card/Tabs, SegmentedControl, Pagination, Field, table toolbar and empty state, page layout, status tones (Badge, Chip/ChipGroup, Banner), List/ListItem and DescriptionList, feedback, responsive Sidebar and Toaster |
| `content` (rank 2) | Markdown (GFM, no raw HTML, safe links including a caller-resolved one, images opt-in, optional source-byte offsets for an annotation layer), bounded collapsible JSON tree, code with lazily-loaded highlighting, whitespace-preserving text, and the `Content` switch over them with `auto` detection and an `inline` one-line variant |
| `dashboard` (rank 3) | Declarative dashboard, point charts, multi-series metric charts, tiles, provenance and sparklines |
| `chat` (rank 3) | Resolved messages and injected send action |
| `table` (rank 3) | DataTable driven by an injected TanStack table instance |
| `board` (rank 3) | Board: a collection in columns by a field, dragged or moved from a menu, committing nothing |

Composite tiers compose layout; they do not import sibling composites. No tier
fetches data, reads host authentication or imports private host aliases. Host CSS
is authoritative: the kit supplies class names and the Tailwind source that
defines them (`theme.css`, `type-slots.css`), which the host compiles into its one
stylesheet. The kit's compiled `preview.css` is for a preview with no host and
is never loaded beside the host's
([README](./README.md#previewing-a-solution-without-a-host)).

## Stories and coverage

Owner stories live in `stories/*.stories.tsx` beside this package's `src/`.
They are ordinary CSF exports with a default title and named render functions.
The SaaS-domain package maintains its stories separately. Neither owner's
preview fixtures enter the package declaration build or production exports.

Every inventory entry records explicit story file/export references or a
**missing** coverage state. The inventory guard detects unaccounted source
families and stale story references. DOM render checks do not establish browser,
external skin, provider workflow or visual regression coverage; those remain
separate evidence. See the migration document for outstanding closure work.

## Type and control geometry

No component here carries a raw `text-*`, `font-*`, `leading-*` or `tracking-*`
utility, and no control carries a literal height. A component names its **type
slot** (`type-card-title`) or its **control rung** (`control-sm`); the skin
decides what either renders as. The vocabulary — an ordinal scale, the roles
that point into it, the slots that point at roles, and the rungs — is
[TOKENS.md](./TOKENS.md), and `src/__tests__/no-raw-type-primitives.test.ts`
refuses a primitive as a default-deny, the same way colour is already refused.

For a control the type belongs to the RUNG rather than to a size variant: a
caller choosing `size="sm"` chooses the height, the inline padding, the label's
type and the glyph size together.

## Conventions

Controls retain `className`, React 19 refs and their native event props. Base UI
composition uses `render`, with `value/defaultValue` or `open/defaultOpen` state.
Data-in Card/Tabs keep their documented prop contracts and compose the same
lower-level controls. Intentional API differences are documented in the migration
notes. Module consumers import public package subpaths and share those exact
subpaths as versioned federation singletons.
