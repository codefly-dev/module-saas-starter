# Shared UI migration

`ui-inventory.json` records the merged baseline by source family, its imports,
owner, disposition and story coverage. An empty story list is a coverage gap,
not an exclusion. The merged source contains no explorer stories; previously
reported local previews are not release evidence. Package names in current
manifests use `@codefly-dev/saas-ui`.

## API decisions

- Preserve the host compound Card API as `CardRoot` with CardHeader, CardTitle,
  CardDescription, CardAction, CardContent and CardFooter. The existing data-in
  `Card` composes that root. Host compatibility files alias CardRoot to Card.
  This avoids an ambiguous union of DOM title attributes and ReactNode titles.
- Preserve compound Tabs as `TabsRoot`, TabsList, TabsTrigger and TabsContent.
  The data-in `Tabs` composes the same Base UI implementation. Its active/initial
  selection, invalid-selection recovery and keepMounted contract remain intact.
  Compound APIs use value/defaultValue/onValueChange and Base UI render props.
  Forward orientation to the underlying root, not only to a data attribute.
- Move Command, InputGroup and Sheet without changing their public composition.
  Their dependencies belong to the package that implements them.
- Sidebar presentation belongs to the kit. Responsive detection is reusable;
  cookie persistence remains a host callback. Toaster accepts theme from its
  host adapter. Neither package reads authentication or application contexts.
- Existing compatibility import paths remain thin re-exports for host source
  stability. New consumers use package exports; no private aliases are required.
  Remove these aliases only in a separately announced host source migration.
- Keep the existing CSS authority and token vocabulary. No package adds global
  CSS. Native refs and DOM events follow React 19; Base UI controls retain their
  own typed event details and focus behavior.

## Completion evidence

The inventory is an accounting tool, not a claim of completion. Missing stories,
provider-dependent previews, skin/browser checks, packed consumption and live
SaaS smoke checks must be recorded before declaring catalog coverage complete.

## Package and runtime contract

The UI packages advance together to **0.3.0**; the SDK remains independently
versioned. `@codefly-dev/ui/table` has composite rank 3, depends on rank-1 layout,
and exports the injected TanStack `DataTable`. It emits declarations using the
existing package build. The host shares every exported UI subpath, including
table, as a versioned federation singleton; the exported-subpath guard prevents
future omissions. Generic remotes must share those same exact subpath keys.

The existing `dashboard` tier also owns `MetricAreaChart`, `MetricLineChart`,
`MetricBarChart`, metric tiles, metric state and provenance, and Sparkline.
Multi-series charts retain label-union alignment, sparse-series gaps, hover
readout and a screen-reader data table. The existing `AreaChart`/`LineChart`
point API remains distinct: it serves the declarative dashboard's single-series
contract. Both use the same linear scale. Tick policies intentionally differ
for degenerate domains and default tick counts; combining them would alter axes.

The SaaS datasource package consumes the kit's controls and table elements while
retaining its typed client, validation schema and mutation ownership. Host CSS
scans both owner packages. Improving metric deltas use the semantic primary
color instead of a fixed green; direction and numeric sign still convey meaning.

Base UI permits focusing disabled tabs without activating them. Compound tabs
retain this behavior. The data-in Tabs adapter activates on focus, recovers an
invalid requested id to the first tab, and immediately hides/unmounts inactive
panels rather than waiting for Base UI's optional exit transition.

## Validation and outstanding closure work

The packed-consumer check is `npm run test:published-ui`. It installs actual
package tarballs in a temporary consumer, typechecks public imports, bundles the
browser-oriented SDK dependency graph, and renders shared controls. It does not
claim unbundled Node ESM support for the generated SDK or registry publication.

Supplementary browser checks used the host's `globals.css` and
`appearanceStyleProperties(DEFAULT_FRONTEND_APPEARANCE)` in a temporary Vite
harness. 47 owner stories rendered in light/dark modes at 390px and 1280px
(188 combinations) without runtime errors. Sheet Escape/focus restoration and
vertical tab keyboard navigation passed. Axe checked seven representative
surfaces across the same four combinations (28 checks), with no WCAG 2 A/AA or
2.1 AA violations. These are default-appearance checks, not external-skin or
external-explorer evidence, and screenshots are not reviewed visual baselines.

This work is **not ready to close the issue**. The inventory still records missing
stories, including host page/provider workflows and runtime surfaces. The external
explorer checkout and externally supplied skin packages were not available in the
checkout; their configuration, composed discovery, static build and skin matrix
have not been validated. Full-page billing, authentication/onboarding,
privacy/consent, notifications, platform operations and editor previews remain
outstanding. Owner-defined presentation replacement/fallbacks need explicit
browser evidence in that environment. A real fixture-identity SaaS smoke run,
required CI and reviewed visual baselines remain release prerequisites. These
requirements remain in the original work item; this document does not defer them
out of scope or claim the issue can close on the component migration alone.

An additional matrix used the repository's two existing generic skin fixtures:
seven representative surfaces × two fixtures × light/dark × mobile/desktop
(56 Axe checks). It found and then verified fixes for inactive-tab opacity and
fixture text-token contrast; the rerun has no WCAG A/AA violations. Runtime
switching also reached an already-open Sheet portal and preserved a mounted
Tabs draft. These fixtures supplement, but do not replace, validation with
externally supplied skin packages.
