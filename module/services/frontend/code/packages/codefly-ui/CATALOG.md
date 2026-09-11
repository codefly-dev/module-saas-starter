# Component catalog

The merged implementation is tracked by the host's
[UI inventory](../../ui-inventory.json) and
[migration decisions](../../UI_MIGRATION.md). The earlier catalog proposal was
not evidence that all primitives, stories or production callers had migrated.

## Public presentation tiers

| Tier | Contract |
| --- | --- |
| `layout` (rank 1) | Native and Base UI controls, compound CardRoot/TabsRoot, data-in Card/Tabs, page layout, feedback, responsive Sidebar and Toaster |
| `dashboard` (rank 3) | Declarative dashboard, point charts, multi-series metric charts, tiles, provenance and sparklines |
| `chat` (rank 3) | Resolved messages and injected send action |
| `table` (rank 3) | DataTable driven by an injected TanStack table instance |

Composite tiers compose layout; they do not import sibling composites. No tier
fetches data, reads host authentication or imports private host aliases. Host CSS
is authoritative; owner packages supply class names, not a second stylesheet.

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

## Conventions

Controls retain `className`, React 19 refs and their native event props. Base UI
composition uses `render`, with `value/defaultValue` or `open/defaultOpen` state.
Data-in Card/Tabs keep their documented prop contracts and compose the same
lower-level controls. Intentional API differences are documented in the migration
notes. Module consumers import public package subpaths and share those exact
subpaths as versioned federation singletons.
