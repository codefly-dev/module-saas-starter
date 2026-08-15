# Frontend contribution installation

Frontend contributions are immutable external packages. A consumer owns the
package, its contribution document, and the logical service bindings in its
composition descriptor. The Starter owns the application shell and every file
in the resolved base package.

An installation never edits the Starter's `package.json`, `package-lock.json`,
`frontend.config.ts`, route tree, or `service.codefly.yaml`. Codefly resolves
the package versions and runs the Starter-owned composition generator inside a
disposable projection.

## Contribution document

The frontend document is strictly decoded against
`contracts/frontend/v2/frontend-contribution.schema.json`:

```yaml
schema: codefly/saas/frontend-contribution/v2
package: "@example/console-frontend"
version: 1.4.0
export: consoleFrontendPlugin
plugin: example-console
```

The package and version become inputs to the generated install graph. The named
export must be a `FrontendReactPlugin` created with
`@codefly/saas-plugin-react`. Its manifest owns navigation, routes, widgets,
presentation permissions, and logical service requirements. Route and widget
components, models, repositories, controllers, and views stay in that external
package.

Backend locations use a separate typed topology contribution:

```yaml
schema: codefly/saas/topology-contribution/v1
bindings:
  - plugin: example-console
    alias: api
    target:
      module: example
      service: console
```

Only logical module and service identities are accepted. URLs, ports,
credentials, environment keys, and deployment fragments are not expressible.
The public contract rejects missing, extra, and duplicate plugin/alias bindings
when the composed application starts and when the service allowlist is built.

## Generated projection

Core passes the descriptor's validated paths to the package-owned generator:

```sh
GOWORK=off go -C tools run ./cmd/module-compose \
  --module .. \
  --output .. \
  --frontend /consumer/contributions/frontend/console.codefly.yaml \
  --topology /consumer/contributions/topology/console.codefly.yaml
```

The projection contains:

- `services/frontend/code/frontend.install.generated.json`, the exact external
  package versions used to generate the disposable npm install graph;
- `services/frontend/code/src/generated/frontend-contributions.ts`, the only
  generated bridge imported by the Starter host;
- normalized logical topology bindings used by the allowlist and deployment
  compilers.

Running without contribution arguments writes an empty, runnable composition.
Installing, uninstalling, and reinstalling produces the same deterministic
outputs for the same input set.

## Published-package proof

`npm run test:published-plugin-contract` packs the two public Starter contracts,
builds the neutral external reference package, and installs those archives into
a clean consumer directory. It compiles and executes the composed allowlist,
then repeats the check after uninstall and reinstall. No workspace link or
Starter root dependency can make that proof pass.

The neutral reference under `contracts/reference` exercises navigation, a
route, a widget, a model/controller/view boundary, a service requirement, the
protobuf capability contract, settings, permissions, fixtures, and topology.
