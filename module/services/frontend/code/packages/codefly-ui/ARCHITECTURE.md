# @codefly-dev/ui architecture

The north-star for the kit. Consumers are solution `Page.tsx` surfaces that can
get arbitrarily complex (dashboards + chat + tables + forms + bespoke code). The
kit's job is to be rich enough that composing beats hand-rolling — never a cage.
Every new primitive is checked against the layer stack and the invariants below.

## Layer stack (turtles all the way down)

Each tier may only compose the tier below it — never a sibling, never upward.

```
skin        tokens (colors·spacing·type) as DATA        ← single source of truth
layout      Card · Section · Tabs · Text · Input · Avatar · Button   (atoms)
charts      Svg · Scale · Axis · Gridline                            (chart atoms)
dashboard   composes layout + charts
chat        composes layout atoms + SDK stream hook
table/form  composes layout atoms
   ↓
Page.tsx    full freedom: composes any of the above + solution-specific code
```

`charts` currently lives inside `dashboard/` and is extracted to its own tier by
#403. `chat`, `table`, and `form` are composite tiers that sit at the same rank
as `dashboard` (they compose `layout`/`charts`, not each other). `plugin-host`
is not a presentational tier — it is the host-facing plugin-runtime surface and
sits outside this stack.

## Invariants

1. **Compose, never re-inline.** A tier imports the primitive below; it does not
   copy that primitive's markup or class string. Composition flows strictly
   downward through the stack. *(The structural direction — a higher tier never
   imports a lower one's sibling or a tier above it — is enforced by the
   layering guard in `src/__tests__/architecture.test.ts`. The narrower "don't
   paste a primitive's class string" check and the concrete
   `dashboard → layout` de-duplication are #402.)*

2. **Singleton per subpath.** Every kit subpath ships as a Module-Federation
   shared singleton (`singleton: true`, version-pinned) so an arbitrarily
   complex page loads ONE copy of React + each kit module + tokens across the
   host and every remote. That is what lets a heavy page stay fast and visually
   coherent. *(Enforced host-side: the share config in
   `src/solutions/SolutionOutlet.tsx` and the `kit-shared-version` test.)*

3. **Pure, data-in.** Kit components fetch nothing and read no host context;
   data resolves via `@codefly/saas-sdk` (the `runDashboard` / `fromDashboardData`
   pattern) so host and remote render identical output from one package
   instance. *(Enforced by the purity guard in
   `src/__tests__/architecture.test.ts` — no host `@/` imports, no network I/O —
   and by the plugin-free surface check in `src/__tests__/package-contract.test.ts`.)*

4. **Tokens flow through everything.** Color and spacing come from the skin;
   components stroke with `currentColor` / `--primary`, so a skin change
   re-themes the whole catalog for free.

## Boundary

- **Kit** = anything reusable across solutions (chat, dashboard, tables, atoms).
  Pure, themeable, singleton.
- **Page.tsx** = solution-specific composition + business logic + bespoke
  components when the kit genuinely lacks one.

## Keeping it turtles-down

`src/__tests__/architecture.test.ts` is the default-deny guard for this
document: it fails CI if a presentational tier imports a tier above it (or a
sibling composite), or if any kit source reaches for host app code or the
network. The tier ranking there is the machine-readable copy of the stack above
— extend both together when a new tier lands.
