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
   re-themes the whole catalog for free. The token vocabulary — the single set
   of names every tier consumes, with light/dark defaults — is the contract in
   [TOKENS.md](./TOKENS.md); `src/__tests__/token-contract.test.ts` keeps that
   document in lockstep with the contract's `FRONTEND_APPEARANCE_TOKEN_NAMES`
   and `DEFAULT_FRONTEND_APPEARANCE`.

5. **Sealed downward.** A higher layer *composes* what a lower layer ships but
   must not *shadow or replace* it. A solution should not swap in its own
   `<Card>` / `<Dashboard>` / `<Collection>` that the kit or another module would
   then pick up — it renders against the one true instance the owning layer
   publishes. This is what keeps the kit and module UI identical in every
   solution and lets a skin change propagate predictably instead of forking per
   solution. It is distinct from *which* version wins: sealing is that a lower
   layer's component isn't overridden *at all*. *(The mechanism is
   Module-Federation singletons: the host shares every sealed layer package
   (React + kit + each module UI) as a `singleton` in
   `src/solutions/SolutionOutlet.tsx`, so one instance is shared across the host
   and every remote. The seal is cooperative — a remote gets that one instance
   because its own build also shares these packages as singletons rather than
   bundling and registering a competing copy. The `sealed-layers` test asserts
   the singleton flag on every package in the shared set, so a new layer package
   cannot ship shared without it.)*

## CSS ownership — every consumer must scan the kit source

The kit ships **no compiled CSS**. Its components carry Tailwind utility classes
as source strings; the class *definitions* are minted by the consuming app's own
Tailwind build (CSS ownership: host authority, core-solutions#48). Tailwind only
emits utilities it can *see*, and it scans its own tree — not `node_modules` — by
default. So **every** Tailwind build that renders a kit component must point
Tailwind at the kit source, e.g. in the host's `globals.css`:

```css
@source "../../packages/codefly-ui/src";
```

Miss this and the primitives still mount with the right markup but **no styles** —
a silent visual break, not a build error, because nothing fails; the classes
simply never make it into the stylesheet. This generalizes to every future
consumer (a solution fe-remote with its own Tailwind build): each one owns the
same `@source` (or an equivalent safelist) for the kit, or the kit renders
unstyled inside it. The host wires this today; a new remote must add it as part
of adopting the kit.

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
