# Generated gateway routes

Status: generated and runtime-active for Connect and REST. Deployment transport
resources are generated separately from the environment topology and ingress
contract.

## Source and artifacts

| Path | Role |
| --- | --- |
| `services/accounts/generated/service-catalog.json` | Descriptor, transport, policy, and owner source. |
| `deployment/topology.bindings.codefly.yaml` | Named Codefly services, endpoints, dependencies, and visibility. |
| `services/accounts/gateway.bindings.codefly.yaml` | Dated compatibility aliases. |
| `services/accounts/generated/gateway-routes.json` | Typed target-neutral `saas.gateway.routes.v1` inventory. |
| `services/accounts/generated/rest-surface.json` | Strict descriptor REST projection. |
| `services/auth-sidecar/code/routing_catalog_gen.go` | Runtime Connect whitelist consumed by the Go gateway and Envoy generator. |
| `services/auth-sidecar/code/routing_rest_catalog_gen.go` | Runtime descriptor REST whitelist. |

Current descriptor output contains 358 public-edge routes:

- 119 canonical Connect procedures;
- 119 exact `customers.*` compatibility aliases rewritten to canonical
  `saas.accounts.v1.*` procedures;
- 120 REST bindings from `google.api.http` annotations;
- zero of the seven `EXPOSURE_INTERNAL` procedures.

## Generation

From `module/services/accounts/code`, after Codefly protobuf generation:

```sh
go generate ./pkg/business
go generate ./pkg/adapters
go generate ./pkg/cataloggen
```

The order is intentional: the gateway compiler consumes the normalized service
catalog. CI runs the same sequence and rejects drift in all three gateway
artifacts.

## Route semantics

Each route records the HTTP method, exact path or bounded path template,
canonical procedure, Codefly module/service owner, named upstream endpoint,
descriptor exposure, and source. Compatibility aliases additionally require a
canonical rewrite and ISO removal-review date.

The gateway compiler resolves the accounts `connect` and `rest` API endpoints
from the deployment topology by API kind. Endpoint names are not repeated in
the gateway binding, so a topology rename changes gateway output in the same
generation pass.

Connect procedures are exact `POST` matches. Simple REST parameters such as
`{org_id}` compile to an anchored `[^/]+` RE2 segment; prefix matches are never
generated. A Google path-template form the compiler cannot represent exactly
fails generation instead of widening the route.

Only `EXPOSURE_PUBLIC` disables edge authentication. Every
`EXPOSURE_AUTHENTICATED` route requires it. `EXPOSURE_INTERNAL`, unspecified
exposure, unknown schema/config fields, duplicate `(method,path)` matches,
invalid endpoints, incomplete aliases, non-uppercase methods, and unsorted
artifacts all fail closed.

## Runtime ownership

`auth-sidecar` no longer walks live descriptors or owns a handwritten public
Connect map. `LoadConnectRoutesFromCatalog` returns defensive copies of the
generated whitelist, then joins every route to the generated authorization
catalog by canonical procedure. This fixed previous drift where public `BeginOAuth`,
`RegisterUser`, and introspection procedures could be classified as protected
even though their descriptor policy was public.

Generated REST entries join policy by canonical procedure. Legacy YAML entries
that match descriptors are parity-checked and skipped; only five explicit
non-protobuf extensions are loaded from YAML. Rate-limit backend failure and
login-factor budgeting use generated method metadata rather than REST/Connect
path comparisons.

The temporary Go gateway key `accounts_connect` maps the owned `accounts`
service plus its named `connect` endpoint. The target-neutral JSON retains those
as separate owner and endpoint fields so runtime gateways, Codefly networking,
and future multi-service aggregation do not need to parse that compatibility
key.

## Compatibility aliases

The pre-v1 `customers.*` namespace remains an explicit configuration entry with
`remove_after: 2026-10-11`. Every alias inherits canonical exposure and rewrites
to the canonical procedure. Removing the mapping requires the usage review and
release process in `CONTRACT_VERSIONING.md`; changing a method policy cannot
leave the alias with stale auth behavior.

## REST and frontend boundary

The running gateway consumes all 120 descriptor REST routes from
`saas.rest.surface.v1`. Non-protobuf billing and magic-link endpoints remain
explicit extensions. Accounts uses the same generated surface for service
registration and a fail-closed method/path allowlist; the checked-in OpenAPI
document is filtered and verified against it. See `REST_SURFACE.md`.

`saas.frontend.plugins.v1` catalogs all 36 Next.js pages and the admin plugin
catch-all. The environment topology generator owns the deployed ingress
VirtualService, so the target-neutral route catalog does not carry a namespace,
gateway, or destination host.
