# Normalized service catalog

Status: active (`saas.catalog.v1`).

The normalized service catalog is the stable handoff between protobuf authoring
and Codefly's route, policy, client, endpoint, and plugin generators. It is
compiled from the registered protobuf file descriptors; it is not another
handwritten service inventory.

## Owned artifacts

| Artifact | Purpose |
| --- | --- |
| `services/accounts/proto/saas/catalog/v1/catalog.proto` | Typed catalog schema shared by Go and TypeScript consumers. |
| `services/accounts/generated/service-catalog.json` | Deterministic accounts catalog checked into source control. |
| `services/accounts/generated/rest-surface.json` | Strict public REST/OpenAPI projection. |
| `services/accounts/code/pkg/business/service_catalog.go` | Fail-closed descriptor compiler and semantic validator. |
| `services/accounts/code/pkg/business/service_vocabulary.go` | Canonical permission, API-key scope, and entitlement vocabulary. |
| `services/accounts/code/cmd/service-catalog` | Reproducible JSON compiler entry point. |
| `services/accounts/code/cmd/frontend-catalog` | Deterministic TypeScript client/vocabulary generator. |
| `services/accounts/code/cmd/deployment-topology` | Deterministic Codefly topology and NetworkPolicy generator. |
| `services/accounts/code/cmd/frontend-plugins` | Deterministic Next.js route and plugin-navigation generator. |
| `services/frontend/code/src/gen/saas/accounts/v1/frontend_catalog.ts` | Typed accounts clients and frontend authorization/product vocabulary. |
| `services/frontend/generated/plugin-catalog.json` | Typed frontend page/plugin/navigation inventory. |
| `deployment/generated/service-topology.json` | Typed module endpoint/dependency/egress inventory. |
| `services/accounts/code/pkg/business/service_catalog_test.go` | Round-trip, drift, ordering, and unsafe-input tests. |

`schema_version` versions the catalog format. `api_version` versions the
service API described by the catalog. They are deliberately independent: an
additive catalog-format release does not change an accounts procedure, and an
additive accounts release does not require a new catalog schema.

## Generation pipeline

Run the pipeline from `module/services/accounts`:

```sh
codefly generate proto --proto ./proto --output . --local --template buf.gen.local.yaml
cd code
go generate ./pkg/business
go generate ./pkg/adapters
go generate ./pkg/cataloggen
```

The first command makes Codefly generate the Go, gRPC, Connect, grpc-gateway,
OpenAPI, and TypeScript bindings using exact local plugin versions. This also
generates the `saas.catalog.v1` types. The second command compiles the loaded
descriptor graph into `generated/service-catalog.json` and refreshes
`AUTHZ_MATRIX.md`. The third joins the catalog with strict Connect
implementation bindings and emits registration plus interface assertions. The
fourth command generates typed authorization/PDP metadata, the auth-sidecar
policy lookup, target-neutral gateway routes, auth-sidecar Connect/REST wiring,
strict accounts REST registration, filtered OpenAPI, exact Istio matches, and
the frontend client/vocabulary catalog. It also joins descriptor protocol
requirements with the strict module topology and writes the actual Codefly
manifests, normalized deployment catalog, and NetworkPolicies. CI repeats all
commands and rejects any diff. Finally, it discovers the Next.js page tree and
joins it with strict plugin/navigation bindings to generate the typed frontend
plugin catalog and runtime inputs.

Do not invoke the catalog command against stale protobuf bindings. Do not hand
edit generated JSON. A generator that consumes the catalog must deserialize the
generated `saas.catalog.v1.ServiceCatalog` type and call the equivalent of
`ValidateServiceCatalog` before emitting output.

## Catalog contract

The root identifies the owning Codefly module/service, API package, API
version, sorted protobuf services, a flat sorted method inventory, and the
canonical permission and entitlement vocabularies. Every method contains:

- its canonical `/package.Service/Method` procedure and request/response type;
- client/server streaming shape, deprecation state, and source proto path;
- the canonical gRPC and Connect transports plus every explicit REST binding;
- the complete typed `saas.policy.v1.MethodPolicy` and compact compatibility
  tier;
- editorial description and per-method module/service ownership.

Each permission records its canonical resource/action split, description,
built-in role projection, and whether it is an API-key scope. Each entitlement
records a finite feature/quota kind, unit, and description. Method permissions
and scopes must resolve against those root definitions.

The flat method inventory is the canonical downstream generator input. The
service grouping is an index and must cover that inventory exactly once.
Codefly can merge catalogs from multiple services because ownership travels
with every method.

## Fail-closed invariants

Compilation fails when any of these conditions is true:

- an RPC is missing policy, contains invalid policy, has an unknown compact
  tier, or lacks descriptor identity/provenance;
- a permission is unknown, malformed, duplicated, unsorted, or used as an API
  key scope without being explicitly eligible;
- an entitlement is malformed, duplicated, unsorted, or has an unknown kind;
- a procedure or `(HTTP method, path)` pair is duplicated;
- one service catalog mixes protobuf API packages;
- method/service inventories are empty, duplicated, unsorted, or disagree;
- method ownership differs from root ownership;
- gRPC or Connect is absent, or REST protocol declaration disagrees with its
  HTTP bindings;
- an internal procedure declares an HTTP binding;
- the schema version is unknown.

There are intentionally no timestamps, machine-specific absolute paths, or
maps in the emitted shape. Repeated compilation of the same descriptor graph is
byte-identical.

## Consumer rollout

The catalog establishes one reviewable boundary; downstream generators replace
manual inventories incrementally:

1. Connect registration and handler-interface glue (`P1-GEN-002`, complete).
2. Envoy/Istio exact routes and upstream ownership (`P1-GEN-003`, complete).
3. Auth/PDP snapshots and documentation (`P1-GEN-004`, complete).
4. Opt-in REST/OpenAPI selection (`P1-GEN-005`, complete).
5. TypeScript clients and permission/entitlement constants (`P1-GEN-006`,
   complete).
6. Codefly endpoints, dependencies, and network policies (`P1-GEN-007`,
   complete).
7. Frontend plugin route/navigation inputs (`P1-GEN-008`, complete).

Each producer must land with a parity test against the catalog before its old
manual source is deleted. Runtime authorization remains fail-closed throughout
the migration.

The TypeScript projection is documented in `FRONTEND_CATALOG.md`. It derives
service imports from descriptor provenance, detects identifier collisions, and
emits no network transport singleton: the frontend passes its configured
Connect transport to the generated client factory.

The deployment projection is documented in `DEPLOYMENT_TOPOLOGY.md`. Its
strict binding owns all Codefly endpoints, endpoint-scoped dependencies,
module-interface exports, deployment ports, and public egress. The accounts
service must expose every protocol present in the descriptor catalog.

The compile-time page/plugin/navigation projection is documented in
`FRONTEND_PLUGINS.md`. It discovers Next.js pages, validates finite navigation
metadata and permission references, and feeds the existing plugin registry,
sidebar, command palette, and user menu without defining the future signed
plugin manifest.

REST selection and publication are documented in `REST_SURFACE.md`. A Google
HTTP annotation is an explicit public-edge opt-in; internal methods carry no
annotation. The strict `rest_bindings.yaml` selects generated versus plugin
registration without becoming a second route inventory.

Connect implementation wiring lives in
`services/accounts/code/pkg/adapters/connect_bindings.yaml`. It is deliberately
not transport inventory: the catalog supplies all services and procedures. The
strict `v1` binding schema only selects `grpc` plus a `GrpcServer` field,
`business`, or `singleton` plus a zero-argument provider. A business-backed
Connect handler may declare one finite `grpc_server` field when a transitional
native gRPC implementation also exists. The same binding generates the 15
native raw-gRPC registrations and derives the nine Connect-only omissions;
P1-NET-001 removes that transitional split. Missing/extra services, unknown
YAML fields, unsupported kinds, and non-identifier names fail generation;
arbitrary Go expressions are not accepted.

## Current editorial exception

Transport and enforcement fields come only from descriptors. Human-facing
descriptions are still joined from `pkg/business/introspection.go` until
`P1-DOC-001` moves them to compiler-readable source comments. The compiler
rejects a missing description, so this exception cannot silently omit a new
RPC from review.
