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
| `services/accounts/code/pkg/cataloggen/testdata/network-policy.golden.yaml` | Test-only topology-policy golden; installed GitOps policies are rendered structurally per environment. |
| `services/accounts/code/pkg/cataloggen/testdata/mesh-policy.golden.yaml` | Test-only mesh-policy golden (STRICT mTLS + the internal-authority and internal-HTTP AuthorizationPolicies + waypoint); mirrors those resources from the per-environment GitOps mesh baseline, not the whole of it — the namespace `default-deny` and the L4 internal policies are rendered there only. |
| `services/accounts/code/pkg/cataloggen/deployment_topology.go` | Strict compiler, semantic validator, and renderers. |

The normalized inventory currently contains eleven services, 16 endpoints,
nine dependency edges, three module-interface endpoints, and four explicit
public-egress grants. The accounts descriptor catalog is an input: if its RPCs
use gRPC, Connect, or REST without a corresponding accounts endpoint,
generation fails.

## Service graph

| Caller | Target endpoints | Kubernetes ports |
| --- | --- | --- |
| `accounts` | `cache/read`, `cache/write` | TCP 6379 |
| `accounts` | `store/tcp` | TCP 5432 |
| `accounts` | `telemetry/grpc` | Codefly-assigned OTLP gRPC port |
| `accounts` | `vault/http` | TCP 8200 |
| `auth-gateway` | `accounts/connect`, `accounts/rest`, `accounts/grpc` | TCP 8080, 9090 |
| `auth-gateway` | `cache/write` | TCP 6379 |
| `frontend` | `auth-gateway/rest` | TCP 8080 |

The Codefly module interface exposes the public `frontend/http` and
`marketing/http` endpoints; the auth-gateway gRPC ext-authz endpoint has module
visibility. Istio routes apex/`www`/docs hosts to `marketing/http` and `app` to
`frontend/http`. Accounts, frontend, marketing, and telemetry may reach
public IP space only over TCP 443. The public rules exclude private, loopback,
link-local, metadata, documentation, benchmark, multicast, and other
special-purpose IPv4/IPv6 ranges. Temporal's gRPC frontend and HTTP UI remain
module-visible without a public ingress route.

## Network-policy model

The topology-policy golden contains 25 `NetworkPolicy` resources:

- one namespace-wide ingress/egress default deny;
- DNS and Istio control-plane egress for all injected workloads;
- Istio ingress only to the public frontend and marketing HTTP ports;
- target ingress and caller egress policies for every declared dependency;
- HTTPS public egress only for accounts, frontend, marketing, and telemetry.

There is no `allow-intra-namespace` rule. Adding a service dependency or port
requires changing the topology binding and reviewing both generated directions
of the edge.

Pod selectors use the `app: <service>` labels emitted by the pinned Codefly
agents. Services whose agent uses a different Kubernetes identity declare its
Service name and app label in the topology. Endpoint ports are explicit
deployment bindings because Codefly's logical endpoint manifest does not carry
Kubernetes ports. Agent port, Service-name, or label changes must update the
topology binding in the same change; pinned deployment schema/render validation
is tracked by `P1-CI-004`.

The generated Codefly dependency declarations contain exact endpoint
references. Accounts resolves `telemetry/grpc` through the SDK and passes that
exact Codefly-owned address to both tracing and metrics; no product
configuration owns a local collector port. The generated NetworkPolicy remains
the hard endpoint/port enforcement boundary.

The AWS overlay replaces stateful services with managed dependencies. A
pod-selector rule cannot authorize an RDS, ElastiCache, external Vault, or S3
address. Production overlays must add narrowly scoped VPC endpoint/security
group or CIDR policy for those configured destinations; the base deliberately
does not open private address space globally.

## Mesh security baseline

Alongside the NetworkPolicy layer, every environment renders an Istio mesh
baseline into `istio-mtls.yaml`:

- one mesh-wide `PeerAuthentication` in `STRICT` mode (no `PERMISSIVE`
  fallback), so all in-namespace traffic is authenticated mTLS;
- one namespace-wide `default-deny` `AuthorizationPolicy` (an empty ALLOW that
  matches nothing), then explicit per-dependency and per-ingress allows layered
  on top of it;
- the namespace carries `istio.io/dataplane-mode: ambient`, so every pod is
  captured by the mesh and an un-injected pod cannot receive traffic.

mTLS authenticates the **workload** (its SPIFFE service account), not the end
user or tenant. It is the reach gate only and never replaces the app-layer
identity/authz gates (`requireInternalCredential`, JWT/OBO user checks), which
remain as defense-in-depth on top of the mesh.

### Internal-authority reach gate

For every service that owns `EXPOSURE_INTERNAL` methods (read from its
`authz-methods.json`), the baseline adds an `allow-<service>-internal-authority`
`AuthorizationPolicy` that ALLOWs those gRPC method paths only from the
allowlisted in-mesh caller identity, denying every other source principal by
default — the ingress-gateway service account included. Istio matches by request path, so the internal
authority surface is gated by caller workload identity even while it stays
multiplexed on the shared HTTP port; no dedicated port is required.

Because that match is L7 and the ambient `ztunnel` enforces L4 only, a
namespace `waypoint` Gateway (`gateway.networking.k8s.io/v1`,
`gatewayClassName: istio-waypoint`) is provisioned and the namespace opts in via
`istio.io/use-waypoint`, so the path match is actually evaluated. The test-only
`mesh-policy.golden.yaml` mirrors these resources.

Two consequences of that data plane are load-bearing, and getting either wrong
is silent:

- **An L7 policy is attached with `targetRefs`, never a `selector`.** A selector
  sends the policy to `ztunnel`, which cannot evaluate L7 rules and fails safe by
  turning the policy into a blanket DENY on the workload it selects. Both
  generated L7 policies therefore carry `targetRefs: [{kind: Service, …}]` naming
  the Kubernetes Service that routes through the waypoint, which also keeps them
  workload-scoped rather than namespace-wide.
- **The L4 port allowlists admit the waypoint's identity.** Once a waypoint
  fronts the namespace, an east-west request arrives at the destination's
  `ztunnel` from the waypoint rather than from the caller, so
  `allow-<service>-from-internal-topology` lists
  `cluster.local/ns/<namespace>/sa/waypoint` alongside the declared callers.
  Caller identity is still enforced, one layer up, by the L7 policies the
  waypoint evaluates.

### Cluster-internal HTTP routes

That reach gate is derived from `authz-methods.json`, so it covers exactly the
catalog owner's `EXPOSURE_INTERNAL` **gRPC procedures**, and it renders nothing
for a route that carries the same authority over HTTP. No catalog describes such
a route and no layer below the mesh can separate it: `frontend/http` is a public
module export, so the frontend's cluster-internal routes — `POST`/`DELETE
/api/solutions/register`, and the token-gated `GET /api/internal/solutions` the
proxy reads its CSP origins from — share TCP 3000 with every browser-facing
page, a NetworkPolicy selects pods and ports rather than paths, and the
port-wide `allow-istio-ingress-to-frontend` cannot subtract one path from the
grant it makes. Such routes are therefore authored on the service, which must
serve HTTP for the match to mean anything:

```yaml
    internal_http_routes:
      - path: /api/internal/solutions
        methods:
          - GET
      - path: /api/solutions/register
        methods:
          - DELETE
          - POST
```

For each one the baseline renders `deny-<service>-internal-http`, an
`AuthorizationPolicy` with `action: DENY` attached to that service's waypoint,
matching those methods on the declared path from every principal outside the
service's declared in-mesh callers (`notPrincipals`). It is a different resource
from the internal-authority ALLOW above and gates a different surface; the two
share only the waypoint that evaluates them. Istio evaluates DENY before ALLOW,
so it subtracts from the port-wide grant; other methods on the same path — the
unauthenticated `GET /api/solutions/register` the sidebar polls — are untouched,
as is the proxy's own loopback read of `/api/internal/solutions`, which never
leaves the pod and so is never captured by the mesh. Each path is emitted twice,
exactly and as a `*`-prefixed suffix pattern, because Istio's default path
normalization does not merge duplicate slashes and `//api/solutions/register`
would otherwise miss the match while the application still served it.

**This gate is east-west only, and the boundary on the public front door is the
route's own credential.** A waypoint never sees ingress-originated traffic:
Istio delivers it straight to the workload unless the install sets
`istio.io/ingress-use-waypoint` on the Service or Namespace *and* runs istiod
with `ENABLE_INGRESS_WAYPOINT_ROUTING` (default `false`) — a mesh-install
setting no module can render. The ingress-gateway principal is therefore exempt
from the deny by construction rather than by omission: naming it would produce a
rule that reads as a north-south boundary and enforces nothing. Registration
arriving through the public ingress is the supported path, and what narrows it
is the credential the route checks, not workload identity — binding that
credential to a publisher is audit A13's work. Percent-encoded separators are
matchable by neither Envoy nor this policy, which is the same conclusion from
the other direction: the route's credential check is the gate, and the mesh
policy contains lateral movement rather than replacing it.

"Cluster-internal" accordingly still names the credential rather than the
reachability of the public front door. What the deny adds is that a workload
holding that credential cannot spend it against the frontend from inside the
namespace.


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
25 NetworkPolicy golden documents. After the module generator creates the
consumer-owned GitOps tree, render an environment with:

```sh
kubectl kustomize modules/<module>/deployment/kustomize/overlays/<environment>
```

CI regenerates and clean-diff checks the normalized catalog, module manifest,
all eleven service manifests, and NetworkPolicy file. A separate CI job copies
only marketing into an isolated build context, installs its own dependency
lock, and runs unit, content, boundary, build, budget, and degraded-product
smoke checks.

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
