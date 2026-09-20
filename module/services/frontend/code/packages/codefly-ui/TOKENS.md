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

## Four layers, not one token bag

Colour reskins because every layer names a token rather than a value. Type,
weight and control geometry never got that treatment — they are raw utilities
compiled into component source, and in one class list the two are
indistinguishable:

```
bg-primary text-primary-foreground   ← semantic, reskins
text-sm font-medium h-8 px-2.5       ← primitive, welded in
```

Collapsing four different things into one namespace is what makes a token set
look unable to carry a real design system. Separated, most of the apparent
conflict is missing indirection:

| Layer | What it is | Example |
| --- | --- | --- |
| **1 · Scale** | ordinal values, no meaning | step `3` is `0.875rem` |
| **2 · Roles** | a named bundle pointing into the scale | `menu-item` is step 3 at `1.25rem` |
| **3 · Slots** | which role a surface uses | `command-item` uses `menu-item` |
| **Rungs** | the same pattern for control geometry | `sm` is 7 spacing units tall |

A role is a pointer *into* the scale, so roles → scale is total and lossless and
the reverse is never needed: a customer can name `Display 01` while the ramp
stays ordinal underneath, and two design-system flavours become two role maps
over one scale — which is exactly what a single `fontSizeBase` multiplier cannot
express. Slots are the kit's own `data-slot` names, so layer 3 wires a
convention that already exists in the source rather than inventing one.

**Every field of a role is optional**, deliberately: a role declares only the
properties it decides. `emphasis` is a weight and nothing else, because
`table-head` sets `font-medium` today and inherits its size. A property the role
leaves out is absent on both sides — the host never projects its custom property
and the generated utility never declares it — so whatever else styles the
element keeps doing so. Declaring it over an unset variable would *not* be the
same as leaving it out: such a declaration still wins the cascade and only then
computes to inherit, wiping a `font-mono` or `tracking-wide` beside it.

That is why **the set of properties a slot decides is fixed when the kit is
built**. A skin moves a role's values (any of them, all of them) but the
resolver refuses one that adds a property to a role, or re-points a slot or rung
at a role of a different shape: the utilities are generated once from the
defaults, and the CSS could neither read the extra property nor stop declaring a
missing one.

A component reaches these through one generated utility per slot and per rung
(`type-card-title`, `control-sm`), written by this package's
`scripts/generate-type-utilities.mjs` from the vocabulary below into
`src/skin/type-slots.generated.css`, exported as `@codefly-dev/ui/type-slots.css`
and checked for drift in CI. Every utility is emitted at zero specificity
(`:where(&)`), so a raw utility on the same element (`text-lg` on a card title,
`h-11` on a button) overrides exactly the property it names and the rest of the
slot or rung stands. The kit's `cn` therefore keeps a raw utility *beside* a slot
rather than treating it as a conflict; the only classes it resolves against each
other are two slots, or two rungs that both set a height.

### Layer 1 — the type scale

<!-- type-scale-table:start -->
| Step | Default |
| ---- | ------- |
| `1` | `0.625rem` |
| `2` | `0.75rem` |
| `3` | `0.875rem` |
| `4` | `1rem` |
| `5` | `1.125rem` |
| `6` | `1.25rem` |
| `7` | `1.5rem` |
| `8` | `1.875rem` |
| `9` | `2.25rem` |
<!-- type-scale-table:end -->

### Layer 2 — the roles

<!-- type-roles-table:start -->
| Role | Size | Weight | Line height | Tracking | Family |
| ---- | ---- | ------ | ----------- | -------- | ------ |
| `surface-title` | `4` | `500` | `1.5rem` | — | `heading` |
| `surface-title-snug` | `4` | `500` | `1.375` | — | `heading` |
| `surface-title-tight` | `4` | `500` | `1` | — | `heading` |
| `surface-title-compact` | `3` | `500` | `1.375` | — | `heading` |
| `surface-title-plain` | `4` | `500` | — | — | — |
| `surface-description` | `3` | — | `1.25rem` | — | — |
| `plain` | — | `400` | — | — | — |
| `page-title` | `7` | `700` | `2rem` | `-0.025em` | — |
| `section-title` | `5` | `600` | `1.75rem` | `-0.025em` | — |
| `body` | `3` | — | `1.25rem` | — | — |
| `emphasis` | — | `500` | — | — | — |
| `control-label` | `3` | `500` | `1` | — | — |
| `menu-item` | `3` | — | `1.25rem` | — | — |
| `group-label` | `2` | `500` | `1rem` | — | — |
| `group-label-plain` | `2` | — | `1rem` | — | — |
| `shortcut` | `2` | — | `1rem` | `0.1em` | — |
| `control` | `3` | `500` | `1.25rem` | — | — |
| `control-sm` | `2` | `500` | `1rem` | — | — |
| `control-xs` | `2` | `500` | `1rem` | — | — |
| `control-touch` | `4` | — | `1.5rem` | — | — |
| `metric-value` | `7` | `600` | `2rem` | `-0.025em` | — |
| `metric-value-lg` | `8` | `600` | `2.25rem` | `-0.025em` | — |
| `metric-total` | `9` | `700` | `2.5rem` | `-0.025em` | — |
| `metric-unit` | `3` | `400` | `1.25rem` | — | — |
| `caption` | `2` | `500` | `1rem` | — | — |
| `chart-label` | `1` | — | `1` | — | — |
<!-- type-roles-table:end -->

### Layer 3 — the slots

<!-- type-slots-table:start -->
| Role | Slots that use it by default |
| ---- | ---------------------------- |
| `surface-title` | `sheet-title`, `alert-dialog-title` |
| `surface-title-snug` | `card-title` |
| `surface-title-tight` | `dialog-title` |
| `surface-title-compact` | `card-title-sm` |
| `surface-title-plain` | `card-heading` |
| `surface-description` | `card-description`, `dialog-description`, `sheet-description`, `alert-dialog-description`, `field-description`, `field-error` |
| `plain` | `metric-delta-label` |
| `page-title` | `page-title` |
| `section-title` | `section-title`, `empty-state-title-illustrated` |
| `body` | `body`, `card`, `dialog-content`, `sheet-content`, `section-description`, `input`, `textarea`, `select-trigger`, `command-input`, `command-empty`, `banner`, `table`, `pagination-ellipsis`, `table-toolbar`, `table-empty-state`, `card-metadata`, `table-caption`, `tabs-content`, `avatar-fallback`, `avatar-group-count`, `sidebar-group-content`, `sidebar-menu-button`, `sidebar-menu-button-lg`, `sidebar-menu-sub-button`, `error-state`, `input-group-text`, `input-group-control`, `empty-state-description`, `metric-label`, `chat-message` |
| `emphasis` | `emphasis`, `banner-title`, `table-head`, `table-footer`, `sidebar-menu-button-active`, `error-state-title` |
| `control-label` | `label` |
| `menu-item` | `select-item`, `dropdown-menu-item`, `dropdown-menu-checkbox-item`, `dropdown-menu-radio-item`, `dropdown-menu-sub-trigger`, `command-item` |
| `group-label` | `dropdown-menu-label`, `command-group`, `badge`, `card-eyebrow`, `sidebar-group-label`, `sidebar-menu-badge` |
| `group-label-plain` | `caption-plain`, `select-label`, `tooltip-content`, `avatar-fallback-sm`, `sidebar-menu-button-sm`, `sidebar-menu-sub-button-sm` |
| `shortcut` | `dropdown-menu-shortcut`, `command-shortcut` |
| `control` | `segmented-control-segment`, `tabs-trigger`, `input-group-addon`, `empty-state-title`, `metric-heading` |
| `control-touch` | `input-touch`, `textarea-touch` |
| `metric-value` | `metric-value` |
| `metric-value-lg` | `metric-value-lg` |
| `metric-total` | `metric-total` |
| `metric-unit` | `metric-unit` |
| `caption` | `metric-delta`, `chat-author` |
| `chart-label` | `chart-label` |
<!-- type-slots-table:end -->

### Control rungs

Height, padding and icon are **`--spacing` multiples, not lengths**, so control
geometry follows a skin's density instead of pinning it. Each rung carries the
type role its label uses: for a control, type belongs to the rung rather than to
a slot, because a caller choosing `size="sm"` is choosing the whole rung.

<!-- control-sizes-table:start -->
| Rung | Height | Padding X | Icon | Text role |
| ---- | ------ | --------- | ---- | --------- |
| `xs` | `6` | `2` | `3` | `control-xs` |
| `sm` | `7` | `2.5` | `3.5` | `control-sm` |
| `default` | `8` | `2.5` | `4` | `control` |
| `lg` | `9` | `2.5` | `4` | `control` |
<!-- control-sizes-table:end -->

```jsonc
{
  "appearance": {
    "typeScale": { "7": "1.625rem" },
    "typeRoles": { "page-title": { "weight": "600", "tracking": "-0.04em" } },
    "typeSlots": { "card-title": "section-title" },
    "controlSizes": { "default": { "height": "9", "paddingX": "3" } }
  }
}
```

### Layer 4 — rules

Constraints a design system has **stated**, carried as data and checked at build
time. "A page title appears once" is not a render token, but it is a perfectly
good lint rule, and a written rule is a constraint a checker can enforce.

Rules sit **beside** `appearance`, not inside it. Layers 1 to 3 are consumed at
render time and every value they carry reaches CSS, so they pass the injection
gate; a rule reaches a checker and never a stylesheet, so it has its own schema.
Putting it inside `appearance` would mean widening the thing whose narrowness is
the security property.

```jsonc
{
  "rules": {
    "slots": { "page-title": { "maxPerPage": 1 } },
    "headingOrder": "no-skip"
  }
}
```

`checkSkinRules(root, skin.rules)` from `@codefly-dev/ui/skin` reads a rendered
tree — a test render, a story, a document — through the same `data-slot`
attribute layer 3 points at, and reports what it finds; `assertSkinRules` throws
instead. Only rules something enforces are accepted: a field that validates and
is then ignored reads as a promise, which is worse than its absence. Usage rules
needing a notion of "surface" are therefore not here, because there is no surface
vocabulary to check them against.

## What a skin cannot carry

The ladder's third rung — structurally different component anatomy, a header
that is not this header — is **not** a descriptor field. It is the vetted
micro-frontend tier, with its own admission and trust model, and it is meant to
stay hard to reach.

`remotes` used to be declared on the descriptor as reserved for that rung and was
never resolved. A field nothing reads is worse than an absent one: a descriptor
carrying it validated cleanly while the value went nowhere. It has been removed,
and `checkSkinSurvival` now reports any top-level key the resolver does not read,
so an author who writes one is told rather than left to find out in a render.

Three things stay outside the vocabulary entirely, and saying so is part of being
honest about what it does carry:

- **Taste.** Whether the composed result reads as *right* to the person who owns
  the design. No amount of vocabulary produces that judgement.
- **Rules nobody wrote down.** Layer 4 encodes constraints a system has stated.
  Most large systems keep much of their real rule set as drawings, conventions
  and review habits; those have to be extracted before they can be encoded.
- **The approval.** A design review is a process, not an artifact.

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

## Proving a skin survives

A skin descriptor is written in one repository and validated in another, and the
two halves of that split fail quietly together: the contract validator is
**fail-closed** (a field it does not define throws) while the resolver is
**fail-safe** (it catches and keeps the compiled default). So one unrecognised
key costs a descriptor *every* value it carried, and the only trace is a log line
on a server.

The check for that ships here rather than in each authoring repository, because a
private schema next to the descriptor would pass while the real resolver
disagreed:

```ts
import { assertSkinSurvives } from "@codefly-dev/ui/skin";

await assertSkinSurvives(descriptor, {
  fallback,                    // the compiled default the product overlays
  sources: [mountedFileSource], // the deployment's OWN chain, not a stand-in
  expectSource: "file",        // the product can never silently BE the fallback
});
```

It resolves the descriptor, walks every leaf it declared, and throws naming each
one that did not reach the render along with the source that won.
`checkSkinSurvival` returns the same thing as a report instead of throwing.

Two results are worth knowing before reading a failure:

- **A rejected descriptor reports every leaf**, not the offending key alone. That
  is the fail-closed/fail-safe composition being shown, not a bug in the check.
- **An unsafe `logo.lightSrc` reports the whole `branding.logo`**, because the
  resolver keeps a logo only when its light source passes the asset allowlist —
  the alt text and dark variant go with it.

`FRONTEND_APPEARANCE_FIELD_NAMES` in the contract names the accepted `appearance`
fields, alongside `FRONTEND_APPEARANCE_TOKEN_NAMES` for the per-mode colors.

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

Cross-ref: the token-contract decision recorded by the consuming organization.
