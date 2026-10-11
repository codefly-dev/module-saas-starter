# Generated REST and OpenAPI surface

Status: active (`saas.rest.surface.v1`).

REST is an explicit public-edge projection of the protobuf contract. An RPC is
REST-enabled only when it has a `google.api.http` annotation and its generated
method policy is `EXPOSURE_PUBLIC` or `EXPOSURE_AUTHENTICATED`. Internal RPCs
must not have HTTP annotations and remain available through generated gRPC and
Connect clients.

The installation contract also declares `POST
/v1/installations/{installation_id}:narrow` for organisation administrators. It
accepts `org_id`, `installation_id` and JSON `scope`, logs the narrowing before
commit, increments revision and cancels only that installation's organisation's
work. `POST /v1/installations` accepts optional JSON `scope` bounded by the
module ceiling. These additions require the coordinator's complete regeneration;
the checked-in generated counts below describe the preceding projection.

## Current surface

The accounts projection contains 165 descriptor routes across 29 services
(counted from `generated/rest-surface.json` when the organization-installation
routes of issue #1061 landed; recount from that file, which is the source):

- 18 public routes and 147 authenticated routes;
- 140 OpenAPI paths and 165 operations;
- zero of the 54 internal RPCs;
- fifteen explicit non-protobuf extensions loaded by auth-gateway: magic-link
  request/verification, the billing and Resend email webhooks, the two GitHub
  delivery receivers, the public status surface, the subscriptions stream,
  checkout, free-plan, portal, and the four OAuth 2 authorization-server routes
  (metadata, authorize validation, authorize grant, token).

The generated runtime therefore authorizes 180 REST routes in total. Descriptor
routes and extensions remain separate so an extension can never be mistaken
for a protobuf procedure or inherit policy by path similarity.

The extension count is the one number here a test holds:
`TestGeneratedRESTSurfaceAndExtensions` in `auth-gateway/code/routing_rest.go`'s
test file pins the loaded extension set by method+path, so it cannot change
without that assertion changing. The totals on this page are derived from it by
hand and nothing reads them — which is why the extension count sat at "seven"
from the day `/v1/subscriptions/stream` landed until it was corrected here. Read
the pinned set, not this paragraph, when the answer has to be right.

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
| `services/auth-gateway/routing/rest/saas-starter/accounts/non-protobuf-extensions.rest.codefly.yaml` | Fifteen explicit routes without protobuf ownership. |
| `services/accounts/openapi/api.swagger.json` | Checked-in public OpenAPI document. |

The strict binding file covers every surface service exactly once. Twenty-five
services use generated grpc-gateway registration; `PrincipalService`,
`DelegationService`, `InstallationService` and `WorkContextService` use the
modular `permissions` plugin registration. An
unknown field, missing/extra service, unsupported binding kind, duplicate
plugin, unsafe template, route collision, internal exposure, or ownership drift
fails generation.

## Runtime boundary

Accounts registers only catalog-selected services and wraps grpc-gateway in a
generated method/path allowlist. The allowlist is defense in depth: unknown,
wrong-method, and internal paths return 404 before reaching grpc-gateway. The
transcoder dials the generated Connect port, whose Connect-Go handler serves
Connect, gRPC, and gRPC-Web for all 29 services; it no longer depends on the
incomplete legacy raw-gRPC registration set.

Auth-gateway loads the 165 descriptor routes from generated Go and joins each
one to generated authorization metadata by canonical procedure. One
extension-only YAML file owns the fifteen routes without protobuf procedures.
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
and public documents both have 165 operations; pruning reduces definitions
from 314 to 311.

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

The three executable-artifact consent methods on `ModuleCapabilitiesService`
are internal-only gRPC/Connect methods. They have no REST annotations and are
included explicitly in the module-authority listener. Their request carries a
module credential separately from its current signed parent and an installed
policy selector; neither an ordinary HTTP authorization header nor generic
perimeter access grants consent. See `MODULE_INSTALLATION.md` for the exact
identity, policy and lifecycle contract.
