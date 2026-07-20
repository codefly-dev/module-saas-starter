# Generated deployment topology

Status: active (`P1-GEN-007` and `P1-NET-006`, complete).

`deployment/topology.bindings.codefly.yaml` is the single module-level source
for Codefly services, endpoints, endpoint-scoped dependencies, module exports,
deployment ports, and public egress. The runtime `module.codefly.yaml` and every
`services/*/service.codefly.yaml` are generated outputs and must not be edited.

## Owned artifacts

| Artifact | Purpose |
| --- | --- |
| `deployment/topology.bindings.codefly.yaml` | Strict authored topology and non-topology service settings required to render complete manifests. |
| `deployment/application.bindings.codefly.yaml` | Optional application-owned side file for narrowly typed Postgres migration sources; absent in the generic Starter. |
| `deployment/generated/service-topology.json` | Typed `saas.deployment.topology.v1` inventory. |
| `module.codefly.yaml` | Generated Codefly module interface and service list. |
| `services/*/service.codefly.yaml` | Generated agents, endpoint-scoped dependencies, endpoints, workspace configuration dependencies, and specs. |
| `deployment/kustomize/base/network-policy.yaml` | Generated default-deny and least-privilege Kubernetes policies. |
| `services/accounts/code/pkg/cataloggen/deployment_topology.go` | Strict compiler, semantic validator, and renderers. |

The normalized inventory currently contains seven services, 11 endpoints,
eight dependency edges, two module-interface endpoints, and two explicit
public-egress grants. The accounts descriptor catalog is an input: if its RPCs
use gRPC, Connect, or REST without a corresponding accounts endpoint,
generation fails.

## Service graph

| Caller | Target endpoints | Kubernetes ports |
| --- | --- | --- |
| `accounts` | `cache/read`, `cache/write` | TCP 6379 |
| `accounts` | `object-storage/accounts` | TCP 9000 |
| `accounts` | `store/tcp` | TCP 5432 |
| `accounts` | `vault/http` | TCP 8200 |
| `auth-sidecar` | `accounts/connect`, `accounts/rest`, `accounts/grpc` | TCP 8080, 9090 |
| `auth-sidecar` | `cache/write` | TCP 6379 |
| `auth-sidecar` | `frontend/http` | TCP 3000 |
| `frontend` | `accounts/connect`, `accounts/rest` | TCP 8080 |

The public module interface exposes only `auth-sidecar/http`; its gRPC
ext-authz endpoint has module visibility. Accounts and frontend may reach
public IP space only over TCP 443. The public rules exclude private, loopback,
link-local, metadata, documentation, benchmark, multicast, and other
special-purpose IPv4/IPv6 ranges.

## Network-policy model

The generated base contains 15 `NetworkPolicy` resources:

- one namespace-wide ingress/egress default deny;
- DNS and Istio control-plane egress for all injected workloads;
- Istio ingress only to the public auth-sidecar HTTP port;
- target ingress and caller egress policies for every declared dependency;
- HTTPS public egress only for accounts and frontend.

There is no `allow-intra-namespace` rule. Adding a service dependency or port
requires changing the topology binding and reviewing both generated directions
of the edge.

Pod selectors use the `app: <service>` labels emitted by the pinned Codefly
agents. Endpoint ports are explicit deployment bindings because Codefly's
logical endpoint manifest does not carry Kubernetes ports. Agent port or label
changes must update the topology binding in the same change; pinned deployment
schema/render validation is tracked by `P1-CI-004`.

The generated Codefly dependency declarations contain exact endpoint
references. The currently pinned runtime still injects every endpoint of a
declared dependency into the service environment, so the generated
NetworkPolicy is the hard endpoint/port enforcement boundary until runtime
injection applies those references too.

The AWS overlay replaces stateful services with managed dependencies. A
pod-selector rule cannot authorize an RDS, ElastiCache, external Vault, or S3
address. Production overlays must add narrowly scoped VPC endpoint/security
group or CIDR policy for those configured destinations; the base deliberately
does not open private address space globally.

## Generation and validation

After protobuf generation, run from `services/accounts/code`:

```sh
go generate ./pkg/business
go generate ./pkg/cataloggen
```

The compiler rejects unknown YAML fields and schema versions, invalid or
unsorted services/endpoints/dependencies, unknown endpoint references,
dependency cycles, invalid visibility/API/ports, mismatched module-interface
exports, and missing descriptor-required accounts protocols.

Parity tests build every artifact twice, compare all checked-in outputs, parse
the generated files through Codefly's resource model, and strictly inspect all
15 Kubernetes documents. The deployment trees can be rendered locally with:

```sh
kubectl kustomize module/deployment/kustomize/base
kubectl kustomize module/deployment/kustomize/overlays/local
kubectl kustomize module/deployment/kustomize/overlays/aws
```

CI regenerates and clean-diff checks the normalized catalog, module manifest,
all seven service manifests, and NetworkPolicy file.

Applications that install backend products may add
`deployment/application.bindings.codefly.yaml` without editing Starter topology:

```yaml
version: v1
module_name: installed-saas
postgres_migration_sources:
  - service: store
    name: product_name
    path: ../../../product/services/backend/migrations
```

The optional `module_name` is the installed application identity. The compiler
otherwise accepts only named sources targeting a Starter Postgres service;
it cannot replace arbitrary service specs, endpoints, agents, or dependencies.
Each source receives the Postgres agent's independent
`schema_migrations_<name>` lineage.

That application side file currently extends migration inputs only. It does
not grant an installed product service access to the accounts internal RPC
listener. Generic usage producers require the named internal gRPC endpoint and
generated product dependency edge tracked by `P1-NET-007`; the current mixed
private REST/h2c listener must not be promoted to a module export.
