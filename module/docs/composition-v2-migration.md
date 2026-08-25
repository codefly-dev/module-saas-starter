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

The v2 descriptor points directly at contribution sources; Starter does not
require adapter manifests around frontend directories, protobuf files, or
fixture YAML. The permission document retains its versioned permission schema:

```yaml
kind: composed-module
name: my-product
base:
  id: codefly/saas-starter
  version: ">=0.1.0 <0.2.0"
contributions:
  frontend:
    - {path: frontend, export: productPlugin}
  settings:
    - {path: proto/product/settings/v1/settings.proto, message: product.settings.v1.Settings}
  permissions:
    - {path: permissions.codefly.yaml}
  fixtures:
    - {path: fixtures/local.yml}
```

Codefly hashes these declared inputs, copies the immutable Starter into a fresh
projection, and passes `.codefly/composition.input.json` to the package
generators. The generators stage source files, update the real npm install
graph, regenerate typed settings clients, and emit
`.codefly/composition.catalog.json`. Core rejects a catalog that omits or adds
inputs and resolves its claims against the Starter's base claims before the
projection can run. Fixture inputs ending in either `.yaml` or `.yml` are
normalized to selectable `fixtures/<name>.yaml` files in the projection.
