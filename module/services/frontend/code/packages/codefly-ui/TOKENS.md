# The token contract

One shared token vocabulary, owned here at the kit / skin layer. Every layer
above — the kit's own components, the host module UI, and every solution
Module-Federation remote — consumes these tokens **by name** and hard-codes no
color. That is what lets a single skin swap re-theme the whole cake: change the
values behind the names in one place and everything above re-themes for free.

The vocabulary is the shadcn semantic token set (`--muted-foreground`,
`--destructive`, `--accent`, `--border`, …). The names and their light/dark
default values are enumerated in
[`@codefly/saas-plugin-contract`](../saas-plugin-contract) —
`FRONTEND_APPEARANCE_TOKEN_NAMES` and `DEFAULT_FRONTEND_APPEARANCE`. They live
there, one layer down, because the compile-time appearance validator
(`resolveFrontendAppearance`) consumes them and that contract package must not
depend on the kit — inverting the stack. The kit / skin layer **owns** the
vocabulary in the sense that matters: this document and its drift guard
(`src/__tests__/token-contract.test.ts`, which fails CI if the two ever
diverge) are the contract every layer above reads, and the tokens reach those
layers as CSS-variable names — never as a code import, so a solution consumes
the vocabulary without depending on the (host-internal, unpublished) contract
package at all.

## How a name becomes a color

The chain is one direction, top to bottom — a skin is *data*, never CSS:

1. **Contract** — `DEFAULT_FRONTEND_APPEARANCE` in `@codefly/saas-plugin-contract`
   holds the canonical names and the default (host) `light`/`dark` values.
2. **Skin** — `resolveSkin` (`@codefly-dev/ui/skin`) overlays a validated skin
   descriptor onto that default. Only the tokens a skin declares are overridden;
   the rest inherit the default.
3. **Custom properties** — the host projects the resolved appearance onto
   `<html>` as `--appearance-{light,dark}-<token>` properties, and
   `src/app/globals.css` binds the active mode's set to the public shadcn
   variables (`--muted-foreground`, `--border`, …).
4. **Utilities** — Tailwind maps those variables to utility classes
   (`bg-background`, `text-muted-foreground`, `border-border`, …).
5. **Components** — kit components, host pages, and solution remotes reference
   only those utilities / variables. None of them names a raw color.

## Color tokens

Per-mode color. Any token a skin omits inherits the default below. Light and
dark are two complete palettes; switching mode selects which one the shared
variables read — it does not layer one over the other.

<!-- token-table:start -->
| Token | CSS variable | Role | Light default | Dark default |
| ----- | ------------ | ---- | ------------- | ------------ |
| `background` | `--background` | App canvas background | `oklch(1 0 0)` | `oklch(0.145 0 0)` |
| `foreground` | `--foreground` | Body text on the canvas | `oklch(0.145 0 0)` | `oklch(0.985 0 0)` |
| `card` | `--card` | Raised surface (cards, panels) | `oklch(1 0 0)` | `oklch(0.205 0 0)` |
| `cardForeground` | `--card-foreground` | Text on card surfaces | `oklch(0.145 0 0)` | `oklch(0.985 0 0)` |
| `popover` | `--popover` | Floating surface (menus, popovers) | `oklch(1 0 0)` | `oklch(0.205 0 0)` |
| `popoverForeground` | `--popover-foreground` | Text on popovers | `oklch(0.145 0 0)` | `oklch(0.985 0 0)` |
| `primary` | `--primary` | Primary action / brand fill | `oklch(0.205 0 0)` | `oklch(0.922 0 0)` |
| `primaryForeground` | `--primary-foreground` | Text/icon on the primary fill | `oklch(0.985 0 0)` | `oklch(0.205 0 0)` |
| `secondary` | `--secondary` | Secondary surface / fill | `oklch(0.97 0 0)` | `oklch(0.269 0 0)` |
| `secondaryForeground` | `--secondary-foreground` | Text on the secondary fill | `oklch(0.205 0 0)` | `oklch(0.985 0 0)` |
| `muted` | `--muted` | Low-emphasis surface | `oklch(0.97 0 0)` | `oklch(0.269 0 0)` |
| `mutedForeground` | `--muted-foreground` | Low-emphasis / secondary text | `oklch(0.556 0 0)` | `oklch(0.708 0 0)` |
| `accent` | `--accent` | Hover / accent surface | `oklch(0.97 0 0)` | `oklch(0.269 0 0)` |
| `accentForeground` | `--accent-foreground` | Text on the accent surface | `oklch(0.205 0 0)` | `oklch(0.985 0 0)` |
| `destructive` | `--destructive` | Destructive action / error | `oklch(0.577 0.245 27.325)` | `oklch(0.704 0.191 22.216)` |
| `border` | `--border` | Default hairline border | `oklch(0.922 0 0)` | `oklch(1 0 0 / 10%)` |
| `input` | `--input` | Input control border | `oklch(0.922 0 0)` | `oklch(1 0 0 / 15%)` |
| `ring` | `--ring` | Focus ring | `oklch(0.708 0 0)` | `oklch(0.556 0 0)` |
| `sidebar` | `--sidebar` | Navigation sidebar surface | `oklch(0.985 0 0)` | `oklch(0.205 0 0)` |
| `sidebarForeground` | `--sidebar-foreground` | Sidebar text | `oklch(0.145 0 0)` | `oklch(0.985 0 0)` |
| `sidebarPrimary` | `--sidebar-primary` | Sidebar active / brand fill | `oklch(0.205 0 0)` | `oklch(0.488 0.243 264.376)` |
| `sidebarPrimaryForeground` | `--sidebar-primary-foreground` | Text on the sidebar primary fill | `oklch(0.985 0 0)` | `oklch(0.985 0 0)` |
| `sidebarAccent` | `--sidebar-accent` | Sidebar hover / accent surface | `oklch(0.97 0 0)` | `oklch(0.269 0 0)` |
| `sidebarAccentForeground` | `--sidebar-accent-foreground` | Text on the sidebar accent surface | `oklch(0.205 0 0)` | `oklch(0.985 0 0)` |
| `sidebarBorder` | `--sidebar-border` | Sidebar border | `oklch(0.922 0 0)` | `oklch(1 0 0 / 10%)` |
| `sidebarRing` | `--sidebar-ring` | Sidebar focus ring | `oklch(0.708 0 0)` | `oklch(0.556 0 0)` |
| `chart1` | `--chart-1` | Categorical chart series 1 | `oklch(0.87 0 0)` | `oklch(0.87 0 0)` |
| `chart2` | `--chart-2` | Categorical chart series 2 | `oklch(0.556 0 0)` | `oklch(0.556 0 0)` |
| `chart3` | `--chart-3` | Categorical chart series 3 | `oklch(0.439 0 0)` | `oklch(0.439 0 0)` |
| `chart4` | `--chart-4` | Categorical chart series 4 | `oklch(0.371 0 0)` | `oklch(0.371 0 0)` |
| `chart5` | `--chart-5` | Categorical chart series 5 | `oklch(0.269 0 0)` | `oklch(0.269 0 0)` |
<!-- token-table:end -->

## Structural and typographic tokens

Shared across both modes (not per-palette). A skin sets these once; they drive
the density, corner, type, and elevation scales app-wide.

<!-- structural-table:start -->
| Token | Drives | Default |
| ----- | ------ | ------- |
| `defaultTheme` | Initial mode when the viewer has no preference | `system` |
| `radius` | Corner radius scale (`--radius`, `rounded-*`) | `0.625rem` |
| `fontSans` | Body font (`--font-sans`) | `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif` |
| `fontHeading` | Heading font (`--font-heading`) | `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif` |
| `spacing` | Base spacing unit / density (`--spacing`) | `0.25rem` |
| `fontSizeBase` | Root font size; rescales rem typography | `1rem` |
| `sidebarWidth` | Expanded sidebar width | `16rem` |
| `sidebarWidthIcon` | Collapsed (icon-rail) sidebar width | `3rem` |
| `borderWidth` | Width of the app-wide `border` utility | `1px` |
| `shadowStrength` | Unitless multiplier (0–2) on the elevation scale | `1` |
<!-- structural-table:end -->

## Per-skin overrides

A skin is a validated data descriptor, not code. It carries a **partial**
appearance: any token it declares overrides the default; everything else inherits
the values above. That partiality is the whole point — a skin restates only what
it changes, so the default host palette stays the fallback for every unset token
and a bad or missing value can never break a render.

```jsonc
{
  "appearance": {
    "defaultTheme": "dark",
    "radius": "0",
    "light": { "primary": "oklch(0.52 0.22 285)" },
    "dark":  { "primary": "oklch(0.62 0.22 285)", "accent": "oklch(0.72 0.14 210)" }
  }
}
```

Two complete example skins — one light, one sharp-cornered dark — live in
[`examples/skins/`](../../examples/skins) and are exercised end to end (real
mounted-file source → resolver → contract validator) in
`src/lib/skin/__tests__/example-skins.test.ts`.

## Consuming by name

- **Kit components** reference token utilities only (`bg-card`,
  `text-muted-foreground`, `border-border`, `text-primary`, …) or
  `currentColor`. The layering guard (`src/__tests__/architecture.test.ts`) and
  the primitive guard (`src/__tests__/no-reinlined-primitives.test.ts`) keep the
  kit token-only.
- **Host module UI** and **solution remotes** consume the same Tailwind
  utilities / CSS variables. Because the kit ships as a Module-Federation
  singleton, host and remotes resolve one shared instance and one shared token
  set, so a skin swap re-themes them together.

Cross-ref: the token-contract decision recorded in obin-ai/core-solutions#49.
