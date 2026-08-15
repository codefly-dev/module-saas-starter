# Composition v2 migration

Composition v2 changes ownership, not application behavior. A consumer keeps its
composition descriptor, contribution sources, and generated lock. Codefly resolves
the exact `codefly/saas-starter` release into its content-addressed cache and renders
the runnable module into a disposable projection.

Consumers migrating from a copied Starter must retain `tools/base-manifest.json`,
`tools/base-integrity-allow.json`, and their current `base-source.json` until the v1
three-way classification has completed. Each existing divergence is classified as
Starter-owned, product contribution, generated output, or blocked on a new generic
contract. The copied tree is removed only after the v1 and v2 projections produce
the same service catalog, endpoint graph, frontend inventory, permissions, settings,
fixtures, and runtime smoke results.

The `0.1.x` package line supports migration from Starter `0.0.32` and later. Earlier
copies must first update through the existing preview/apply flow and reach a clean
base-integrity check.
