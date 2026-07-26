# Generated REST and OpenAPI surface

Status: active (`saas.rest.surface.v1`).

REST is an explicit public-edge projection of the protobuf contract. An RPC is
REST-enabled only when it has a `google.api.http` annotation and its generated
method policy is `EXPOSURE_PUBLIC` or `EXPOSURE_AUTHENTICATED`. Internal RPCs
must not have HTTP annotations and remain available through generated gRPC and
Connect clients.

## Current surface

The accounts projection contains 115 descriptor routes across 23 services:

- 11 public routes and 104 authenticated routes;
- 95 OpenAPI paths and 115 operations;
- zero of the seven internal RPCs;
- five explicit non-protobuf extensions loaded by auth-sidecar: magic-link
  request/verification, the billing webhook, checkout, and portal.

The generated runtime therefore authorizes 120 REST routes in total. Descriptor
routes and extensions remain separate so an extension can never be mistaken
for a protobuf procedure or inherit policy by path similarity.

## Sources and generated artifacts

| Path | Role |
| --- | --- |
| `services/accounts/proto/saas/accounts/v1/*.proto` | Per-method REST opt-in and policy source. |
| `services/accounts/generated/service-catalog.json` | Validated descriptor and HTTP-binding inventory. |
| `services/accounts/generated/gateway-routes.json` | Public-edge route and ownership projection. |
| `services/accounts/code/pkg/adapters/rest_bindings.yaml` | Strict service-to-generated/plugin implementation binding. |
| `services/accounts/generated/rest-surface.json` | Typed target-neutral REST catalog. |
| `services/accounts/code/pkg/adapters/rest_registration_catalog_gen.go` | Accounts registration and exact/template allowlist. |
| `services/auth-sidecar/code/routing_rest_catalog_gen.go` | Auth-sidecar descriptor REST inventory. |
| `services/auth-sidecar/routing/rest/saas-starter/api/non-protobuf-extensions.rest.codefly.yaml` | Five explicit routes without protobuf ownership. |
| `services/accounts/openapi/api.swagger.json` | Checked-in public OpenAPI document. |

The strict binding file covers every surface service exactly once. Twenty-one
services use generated grpc-gateway registration; `PrincipalService` and
`DelegationService` retain the modular `permissions` plugin registration. An
unknown field, missing/extra service, unsupported binding kind, duplicate
plugin, unsafe template, route collision, internal exposure, or ownership drift
fails generation.

## Runtime boundary

Accounts registers only catalog-selected services and wraps grpc-gateway in a
generated method/path allowlist. The allowlist is defense in depth: unknown,
wrong-method, and internal paths return 404 before reaching grpc-gateway. The
transcoder dials the generated Connect port, whose Connect-Go handler serves
Connect, gRPC, and gRPC-Web for all 24 services; it no longer depends on the
incomplete legacy raw-gRPC registration set.

Auth-sidecar loads the 119 descriptor routes from generated Go and joins each
one to generated authorization metadata by canonical procedure. One
extension-only YAML file owns the five routes without protobuf procedures.
Startup rejects disabled extension entries and any method/path collision with a
descriptor route, so the file cannot become a shadow descriptor inventory.

## OpenAPI publication

Codefly emits the unfiltered grpc-gateway OpenAPI document to the tracked,
generator-owned `generated/openapi-raw/api.swagger.json`. The REST compiler
verifies every operation against `rest-surface.json`, rejects missing or
unexpected routes, normalizes path-parameter spelling, adds
`x-codefly-rest-schema` and `x-codefly-owner`, prunes unreachable definitions,
and writes the public document to `openapi/api.swagger.json`. The current raw
and public documents both have 119 operations; pruning reduces definitions
from 192 to 191.

## Regeneration

Run from `module/services/accounts`:

```sh
codefly generate proto --proto ./proto --output . --local --template buf.gen.local.yaml
cd code
go generate ./pkg/business ./pkg/adapters ./pkg/cataloggen
```

Codefly generation must run first because the REST compiler deliberately reads
the checked raw generator output instead of trusting a previous public
artifact. CI repeats this pipeline and rejects drift in the typed catalog,
accounts runtime, auth-sidecar runtime, and filtered OpenAPI document.
