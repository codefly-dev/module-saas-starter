# Frontend product plugin installation and removal

Date: 2026-07-16
Status: canonical application procedure
Tasks: FP-007A, FP-010A, FP-065

This procedure is product-neutral. Warden, Mind, Codefly-owned products, and
future applications use the same files and commands. Product names, URLs,
credentials, DTOs, and backend-specific behavior never enter the Starter host.

## Updating an installed Starter base

Never update a consumer with `rsync`, a directory copy, a hand-edited manifest,
or a product-local updater. Codefly owns the update transaction; the Starter
owns only its versioned manifest. From the consumer workspace, first pin the
canonical source with an immutable semantic-version tag:

```sh
codefly sync module <consumer-module-name> \
  --source https://github.com/codefly-dev/module-saas-starter.git \
  --to <starter-version> \
  --subdir module
codefly sync module <consumer-module-name> \
  --source https://github.com/codefly-dev/module-saas-starter.git \
  --to <starter-version> \
  --subdir module \
  --apply
```

The applied command writes `tools/base-source.json` with the repository, tag,
peeled commit, and subdirectory. Later updates need only the new tag:

```sh
codefly sync module <consumer-module-name> --to <next-starter-version>
codefly sync module <consumer-module-name> --to <next-starter-version> --apply
```

Dry-run is always the default. It distinguishes ordinary upstream updates,
consumer-modified base files, newly-canonical paths that collide with
side-additions, files deleted upstream, and files released to overlay ownership.
Generated service manifests, the application `frontend.config.ts`, the frontend
lockfile, allowlisted module identity, and all unrelated side-additions are
preserved. Conflicts fail closed and require an explicit source or consumer
change; there are no overwrite flags. Local source directories are preview-only
so an applied base always has reproducible provenance.

The consumer's `tools/base-integrity-allow.json` is its survival contract. Put
every indispensable product composition root, plugin package entry point, and
integration contract test under `requiredAdditions`. `codefly verify`, module
sync preview, and CI all fail if an update would leave one missing or invalid.

## Supported installation shape

The current reproducible shape is one product-owned npm workspace below:

```text
services/frontend/code/packages/<product-frontend-plugin>/
├── package.json
├── src/
├── test/
└── tsconfig.json
```

The protected application `package.json` already declares `packages/*`; do not
add a product-specific workspace or dependency to it. The product package's own
`package.json` declares its SDK and domain dependencies. npm records the new
workspace and its dependency graph in the application `package-lock.json`.

This keeps installation additive:

- product-owned package directory: handwritten side-addition;
- `frontend.config.ts`: application-owned handwritten composition;
- `package-lock.json`: generated npm install graph;
- plugin service allowlist: generated server-only routing inventory;
- frontend `service.codefly.yaml`: generated Codefly dependency projection.

All other Starter package scripts, dependencies, host source, Docker inputs,
shell, transport, and topology remain protected base files.

## Product package requirements

The package must:

- use a unique npm package name and version;
- build independently;
- import only active entry points from
  `@codefly/saas-plugin-contract` and `@codefly/saas-plugin-react`;
- declare compatible SDK versions in its own package metadata;
- keep product manifests, models, repositories, controllers, views, generated
  clients, fixtures, and domain tests inside the product package;
- contain no Starter-private `@/`, `src/`, `app/`, or `features/` import;
- contain no backend URL, browser-visible deployment variable, credential,
  token accessor, or arbitrary-destination transport;
- export one `FrontendReactPlugin` whose `manifest` is defined through the
  React-free contract and whose lazy components are registered by stable ID;
- declare each backend need as a logical service requirement on that manifest.
- implement the protobuf-defined capability handshake for every declared
  backend endpoint, advertising the same contract ID and major as the manifest.

A minimal REST requirement has this shape:

```ts
services: [
  {
    alias: "api",
    protocol: "rest",
    routePrefix: "/api/v1/product-console",
    compatibility: { contract: "product.console", major: 1 },
  },
]
```

The route prefix is the maximum upstream namespace reachable through the
generic BFF. Keep it as narrow as the installed slice permits.

Route and widget metadata never embeds a component. Bind render code through
the public React package:

```tsx
import { lazy } from "react";
import {
  definePlugin,
  FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import { defineReactPlugin } from "@codefly/saas-plugin-react";

const manifest = definePlugin({
  contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
  name: "product",
  routes: [{ id: "overview", path: "/admin/product" }],
});

export const productFrontendPlugin = defineReactPlugin({
  manifest,
  routes: [{ id: "overview", component: lazy(() => import("./overview.js")) }],
});
```

Every manifest route/widget ID requires exactly one registration. Unknown,
missing, or duplicate registrations fail package/application composition.

Every target backend also implements the canonical source shipped in the
contract package at
`proto/saas/frontend/plugin/v1/capabilities.proto`. Generate native bindings
from that proto. REST targets serve the response at
`/.well-known/codefly/frontend-plugin-capabilities`; Connect targets implement
`FrontendPluginCapabilityService/GetFrontendPluginCapabilities`. Capability
responses contain only schema version `1`, contract, contract major, and sorted
namespaced capability IDs—never tenant data, URLs, versions with secrets, or
deployment details.

The product backend must also run every applicable row in the
[authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md) with
two real tenants and foreign resource IDs. Starter tests certify the BFF half;
they do not certify product claim validation, permissions, ownership, or
storage isolation.

Attach the BFF-provided `x-request-id` to sanitized backend logs without
overwriting it or returning a private correlation value. The
[request-correlation contract](frontend-plugin-request-correlation.md) defines
the retry and support-evidence rules.

Repositories convert failed host responses with the public runtime helper so
the host boundary receives a stable state instead of backend text:

```ts
import { pluginErrorFromResponse } from "@codefly/saas-plugin-react/runtime";

const response = await service.request("traffic");
if (!response.ok) throw await pluginErrorFromResponse(response);
```

Do not display the raw problem title/detail, response body, endpoint, exception
message, or stack. The host independently contains every installed route and
widget and renders its generic retry state. Product views remain responsible
for domain-specific empty, stale, and unauthorized experiences.

## Install

Work from `services/frontend/code` unless a command says otherwise.

1. Add the product package under `packages/*`. Do not edit the protected root
   `package.json`; npm discovers it through the existing wildcard.
2. Regenerate the lock graph with the repository's supported npm version:

   ```sh
   npm install --package-lock-only --ignore-scripts
   npm ci
   ```

3. Import the package export in `frontend.config.ts`, add it once to
   `installedPlugins`, and add one `FrontendServiceBinding` for every declared
   service alias:

   ```ts
   import { productFrontendPlugin } from "@product/saas-frontend-plugin";

   export const installedPlugins = [
     auditPlugin,
     coreUsersPlugin,
     platformAdminPlugin,
     productFrontendPlugin,
   ] as const;

   export const serviceBindings = [
     {
       plugin: "product",
       alias: "api",
       target: { module: "product-module", service: "product-api" },
     },
   ] as const satisfies readonly FrontendServiceBinding[];
   ```

   `module` and `service` are logical Codefly identifiers for an external
   product module. The endpoint is deliberately absent: it is derived from the
   plugin's validated `rest` or `connect` requirement.
4. Generate both application projections:

   ```sh
   npm run generate:plugin-codefly-dependencies
   ```

   This first builds every `packages/*` workspace, then writes the exact
   server-only allowlist, and finally regenerates only the frontend Codefly
   service manifest. It does not rewrite the rest of the deployment topology.
5. Review the generated diff. One logical target produces one external
   dependency even when several aliases use it; its endpoint list is unique and
   sorted. No hostname, URL, port, secret, or bearer may appear.
6. Run the gates:

   ```sh
   npm run check:plugin-codefly-dependencies
   npm run lint
   npm run typecheck
   npm test
   npm run build
   node ../../../tools/base-integrity.mjs check
   ```

   Run `codefly verify` from the consumer workspace and build the frontend
   container as release gates. `npm ci` and the container both install the same
   workspace graph before building all package workspaces.

## Generated dependency chain

```text
product FrontendPlugin manifest service requirement
  + application FrontendServiceBinding
  -> server/plugin-service-allowlist.generated.json
  -> services/frontend/service.codefly.yaml
  -> private server-side Codefly endpoint environment
  -> generic same-origin plugin BFF
```

`frontend.config.ts` is the handwritten application source of truth. The two
downstream files are generated and must never be patched to make a product work.
The full topology generator also consumes the same allowlist, so a later
`go generate ./pkg/cataloggen` preserves the application dependency.

The Node-only `prepare:frontend` gate checks allowlist-to-service-manifest
parity during dev, test, and production builds; container builds do not need a
Go toolchain. The Go generator performs the strict schema and deployment
validation when outputs are regenerated.

## Unavailable backend proof

Keep the package, config entry, allowlist, and generated service dependency
installed, but omit the resolved runtime endpoint only in the test harness.
Requests must stay same-origin and return the generic non-sensitive
`503 backend_unavailable` problem. The product route/widget must contain the
failure as `unavailable`; unrelated shell and Starter contributions must keep
working. Never
fall back silently to fixtures after a live failure.

## Incompatible backend proof

Keep the endpoint reachable but return a different contract/major, malformed
ProtoJSON, or no capability operation. The reserved browser probe must return
the generic `426 backend_incompatible` problem, and only the owning plugin
route/widget may show the retryable incompatible state. The raw handshake body
and backend location must not reach the browser. Restore the exact contract and
major and prove that retry renders the contribution without restarting the
host.

## Uninstall

1. Remove the package import and `installedPlugins` entry from
   `frontend.config.ts`.
2. Remove every binding owned by that plugin.
3. Remove its product-owned `packages/*` directory.
4. Regenerate the npm graph and application projections:

   ```sh
   npm install --package-lock-only --ignore-scripts
   npm ci
   npm run generate:plugin-codefly-dependencies
   ```

5. Run the same gates as installation.

The resulting allowlist and frontend service manifest must contain no product
owner or target. No edit or deletion below Starter `src/` is part of uninstall.

## Mechanical protections

- Base integrity continues to hash the protected root `package.json`, public
  SDK packages, scripts, Dockerfile, generator, and host source.
- The generated lockfile is not byte-hashed, but base integrity validates its
  root metadata, every `packages/*` package's dependency metadata, exact
  workspace set, and npm workspace links. `npm ci` verifies the full transitive
  graph.
- Docker copies `packages/*` before `npm ci` and builds all workspaces with a
  build script.
- The allowlist compiler rejects missing, extra, duplicate, unsafe,
  URL-shaped, protocol-overriding, or incompatible bindings.
- The Codefly generator accepts only strict schema v1 data, external Codefly
  identifiers, safe route prefixes, and the protocol-derived endpoint.
- The build-time dependency gate rejects missing, extra, duplicate, stale, or
  hand-authored external frontend service dependencies.

## Troubleshooting

| Failure | Meaning | Correct action |
| --- | --- | --- |
| `installed service ... has no application binding` | Package declared a service but the application did not map it. | Add the exact plugin/alias binding in `frontend.config.ts`. |
| `binding ... does not match an installed service requirement` | A stale or misspelled binding remains. | Correct or remove the application binding. |
| `plugin service allowlist is stale` | Composition changed without regenerating routing inventory. | Run `npm run generate:plugin-codefly-dependencies`. |
| `frontend service manifest does not exactly match` | Allowlist and Codefly dependency output diverged. | Run the same generator; do not edit YAML. |
| `invalid frontend workspace install graph` | Product package metadata and lockfile differ, or a workspace link is missing/stale. | Regenerate the lockfile and run `npm ci`. |
| `backend_unavailable` | No usable private endpoint reached the server runtime. | Check the generated dependency, Codefly workspace graph, and backend process without exposing its address. |
| `backend_incompatible` | Capability operation is missing, malformed, or does not match the installed contract/major. | Generate bindings from the packaged proto and align the backend response with the frontend manifest; do not bypass the probe. |

Production Kubernetes cross-module NetworkPolicy synthesis requires a
workspace-level join with the target module's namespace and port inventory; it
is tracked separately as FP-007B. Do not add a broad product-specific egress
exception to the generic Starter policy.
