# Contract namespaces and compatibility

Status: active. The v1 namespace migration landed on 2026-07-13.

This module uses one stable, product-neutral protobuf namespace family. The
namespace is intentionally independent of a deployment name (`warden`, `mind`,
or a customer application) so generated contracts remain reusable when Codefly
composes the starter into another workspace.

## Namespace map

| Contract | Protobuf package | Source directory | Generated Go import | Recommended Go alias |
| --- | --- | --- | --- | --- |
| SaaS product API | `saas.accounts.v1` | `saas/accounts/v1` | `accounts/pkg/gen/saas/accounts/v1` | `accountsv1` |
| Gateway auth/PDP API | `saas.gateway.auth.v1` | `saas/gateway/auth/v1` | `auth-sidecar/pkg/gen/saas/gateway/auth/v1` | `gatewayauthv1` |
| Shared method policy options | `saas.policy.v1` | `saas/policy/v1` | generated inside each consuming module | `policyv1` |
| Normalized generator catalog | `saas.catalog.v1` | `saas/catalog/v1` | `accounts/pkg/gen/saas/catalog/v1` | `catalogv1` |
| Durable job primitives | `saas.jobs.v1` | `saas/jobs/v1` | `accounts/pkg/gen/saas/jobs/v1` | `jobsv1` |
| Frontend plugin capability handshake | `saas.frontend.plugin.v1` | `services/frontend/code/packages/saas-plugin-contract/proto/saas/frontend/plugin/v1` | consumer-owned generated output | `frontendpluginv1` |

The accounts contract will be split into bounded-context files under the same
`saas.accounts.v1` package. File boundaries (`identity.proto`,
`organizations.proto`, `billing.proto`, `webhooks.proto`, and so on) organize
ownership and generation without forcing consumers through cross-package type
churn. A bounded context becomes a separate protobuf package only when it has an
independent lifecycle and service deployment.

`saas.catalog.v1` versions the generator handoff rather than a network API. Its
generated Go and TypeScript types define the checked-in service-catalog JSON;
`schema_version` changes independently from the accounts `api_version`. See
`SERVICE_CATALOG.md` for its compatibility and fail-closed consumer rules.

`saas.jobs.v1` is a product-neutral persistence contract rather than an
Accounts network service. It defines the shared inbox/outbox envelope, scope,
lease, attempt, failure, and state-transition vocabulary used by SaaS modules.
Workload-specific RPCs remain in their owning service packages; they reference
or adapt this contract instead of inventing private worker state machines.

`saas.frontend.plugin.v1` is a product-neutral runtime compatibility handshake
published with `@codefly/saas-plugin-contract`. Product backends generate native
bindings from the packaged proto and implement its fixed REST well-known path or
Connect service. The SaaS host compares the returned contract major with the
installed frontend requirement; this namespace does not describe product DTOs
or authorize product operations.

The version suffix is part of every fully qualified gRPC and Connect procedure:

```text
/saas.accounts.v1.AuthService/Authenticate
/saas.accounts.v1.WebhookService/RotateSecret
/saas.gateway.auth.v1.AuthSidecarService/Resolve
```

REST paths retain their existing `/v1/...` form. They do not repeat the package
name. OpenAPI documents use the same major version and release metadata as the
descriptor set.

## Compatibility rules

- A published `v1` package is additive. Existing field numbers, enum numbers,
  message names, service names, and RPC names are never reused or changed.
- Published fields and enum values remain present (deprecated when obsolete)
  for the lifetime of their package major. When omitted from a successor major,
  their names and numbers are reserved there.
- A change that cannot be represented additively requires a new package major
  (`v2`) and an explicit migration adapter or dual-service period.
- The former `customers` and `authsidecar` packages were pre-stable namespaces.
  Their move was one coordinated generated-code, handler, gateway, OpenAPI, and
  frontend-client cutover. Exact `customers.*` procedure aliases are rewritten
  at the edge to `saas.accounts.v1.*`; they remain for at least one minor starter
  release and through 2026-10-11, whichever is later. Removal requires usage
  review. REST `/v1` paths remain compatible throughout. The unused legacy
  auth-sidecar application contract has no public procedure alias; Envoy's
  ext-authz v3 contract is unchanged.
- Once a successor major is released, the previous stable major receives
  security fixes for at least 12 months. Removal requires a dated deprecation
  notice, usage review, and a release note with generated migration examples.
- Prerelease packages use an explicit alpha suffix such as
  `saas.accounts.v2alpha1`; `latest`, date-less experimental names, and package
  names derived from a workspace are forbidden.

## Generation and CI ownership

Buf descriptors are the source of truth. Codefly generation must derive Connect,
gRPC, optional REST/OpenAPI, gateway routes, auth policy metadata, TypeScript
clients, and endpoint/network declarations from the same descriptor graph.
Generated outputs must not invent a different API version.

CI compares each stable descriptor graph with the latest prior release tag that
contains that stable package path,
rejects breaking changes, invokes generation through Codefly, and verifies clean
protobuf/Go/Connect/gateway/OpenAPI/TypeScript outputs. CI uses service-local
Codefly templates whose plugins are exact Go module commands or lockfile-pinned
Node dependencies; registry availability is not a release prerequisite. The
version-pinned BSR templates remain a supported fallback. The same gate moves
with the graph when a successor major lands. The first v1 release intentionally
bootstraps this baseline because `v0.0.2` contains only the pre-stable descriptor
names; exact legacy edge-alias tests cover that transition.
Public-versus-internal exposure is now part of the descriptor-generated policy
snapshot. Remaining lint exceptions are declaration-documentation debt owned by
P1-DOC-001 and request/response naming debt owned by P1-PROTO-005; the latter
requires a compatibility-safe successor instead of renaming stable v1 types in
place. No package-layout exception remains.
