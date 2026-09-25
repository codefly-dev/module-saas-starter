# Generated REST and OpenAPI surface

Status: active (`saas.rest.surface.v1`).

REST is an explicit public-edge projection of the protobuf contract. An RPC is
REST-enabled only when it has a `google.api.http` annotation and its generated
method policy is `EXPOSURE_PUBLIC` or `EXPOSURE_AUTHENTICATED`. Internal RPCs
must not have HTTP annotations and remain available through generated gRPC and
Connect clients.

## Current surface

The accounts projection contains 120 descriptor routes across 24 services:

- 12 public routes and 108 authenticated routes;
- 100 OpenAPI paths and 120 operations;
- zero of the seven internal RPCs;
- seven explicit non-protobuf extensions loaded by auth-gateway: magic-link
  request/verification, the billing and Resend email webhooks, checkout,
  free-plan, and portal.

The generated runtime therefore authorizes 127 REST routes in total. Descriptor
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
| `services/auth-gateway/code/routing_rest_catalog_gen.go` | Auth-gateway descriptor REST inventory. |
| `services/auth-gateway/routing/rest/saas-starter/accounts/non-protobuf-extensions.rest.codefly.yaml` | Seven explicit routes without protobuf ownership. |
| `services/accounts/openapi/api.swagger.json` | Checked-in public OpenAPI document. |

The strict binding file covers every surface service exactly once. Twenty-two
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

Auth-gateway loads the 120 descriptor routes from generated Go and joins each
one to generated authorization metadata by canonical procedure. One
extension-only YAML file owns the seven routes without protobuf procedures.
Startup rejects disabled extension entries and any method/path collision with a
descriptor route, so the file cannot become a shadow descriptor inventory.

## OpenAPI publication

Codefly emits the unfiltered grpc-gateway OpenAPI document to the tracked,
generator-owned `generated/openapi-raw/api.swagger.json`. The proto companion
image is its sole generator. The `google.protobuf` well-known types are embedded
in the `buf` binary rather than pinned by `buf.lock`, and no template here
reaches a contributor's `buf`, so a workstation on a different one emits a
different `google.protobuf.NullValue` description and drifts the file.

The REST compiler
verifies every operation against `rest-surface.json`, rejects missing or
unexpected routes, normalizes path-parameter spelling, adds
`x-codefly-rest-schema` and `x-codefly-owner`, prunes unreachable definitions,
and writes the public document to `openapi/api.swagger.json`. The current raw
and public documents both have 120 operations; pruning reduces definitions
from 195 to 194.

## Regeneration

Run from `module/services/accounts`, with Docker running and a Codefly CLI at
or above 0.1.160:

```sh
codefly generate proto --proto ./proto --output .. --template accounts/proto/buf.gen.yaml
cd code
go generate ./pkg/business ./pkg/adapters ./pkg/cataloggen
```

The first line is the whole proto step. It runs the versioned proto companion
image with the service's own template, `proto/buf.gen.yaml`, which declares
every output the service owns: the Go, gRPC, grpc-gateway and Connect bindings
under `code/pkg/gen`, the raw OpenAPI document under `generated/openapi-raw`,
and the frontend's TypeScript under `../frontend/code/src/gen`. The companion
pins every plugin by its image tag and runs `goimports` over the Go it wrote,
so its output is what `generated-pins-gate` and CI's sync-drift check expect.
`--output ..` is load-bearing: the companion mounts only the `--output`
directory, so it must be `module/services` for the template to reach the
frontend tree beside accounts (a template path is relative to `--output`, and
runs in its own directory). Run it for every proto change, REST or not; a change
that does not reach the REST surface simply leaves `generated/openapi-raw`
byte-identical.

**The two-step spelling documented before CLI 0.1.160 no longer works.**
`codefly generate proto --proto ./proto --output . --local --template
buf.gen.local.yaml` now runs that template *inside* the companion too
(`--local` only selects the file). Its `go run …@version` plugins then download
inside the container and are killed, and its `npx --prefix ../frontend/code`
plugin cannot see a directory outside the `--output` mount. It fails without
writing anything. Do not delete `buf.gen.local.yaml`: `generated-pins-gate`
still reads the plugin versions it checks the generated Go against from it, so
it must keep naming the versions the companion image runs. (The gate's own
failure message still prints the old command; follow this section instead.)

Before changing any proto, run the command on the unchanged tree and require
`git status` to come back clean. That separates "my change churned the tree"
from "my toolchain does not match CI" (root `AGENTS.md`).

Codefly generation must run first because the REST compiler deliberately reads
the checked raw generator output instead of trusting a previous public
artifact. CI repeats this pipeline and rejects drift in the typed catalog,
accounts runtime, auth-gateway runtime, and filtered OpenAPI document.
