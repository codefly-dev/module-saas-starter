# The internal transport a module reaches the host on

The host serves two tiers of RPC. The tenant tier answers for a signed-in
person or an API key and is reached through the gateway. The internal tier —
every method whose `saas.policy.v1.MethodPolicy` carries
`exposure: EXPOSURE_INTERNAL` — answers authority questions about arbitrary
principals: the module-facing capability surface, the Work Context exchanges,
the permission oracles, usage consumption. This document is the contract for
the second one: what carries it, who may connect, what a caller presents, and
what a client library must not require.

It exists because the transport changed. The host used to serve part of this
tier on dedicated server-TLS listeners of its own, on three module endpoints of
their own. Those listeners were removed together with the private execution
surface they were built for; the endpoints left the topology with them. A
client written against them cannot connect to a current host, and the failure
is not a misconfiguration to work around.

## The decision

**In-cluster transport security is the mesh's, not the service's.** The
internal tier is cleartext HTTP/2 — h2c — multiplexed onto accounts' private
`rest` endpoint. The accounts service holds no server certificate, terminates
no TLS of its own, and will not grow a listener that does.

The alternative was a restored server-TLS internal listener on a named
endpoint of its own. It was rejected on three counts:

- **It authenticates the wrong end.** Server TLS proves the *server* to the
  caller. The question this tier has to answer is which *caller* is connecting,
  and the mesh already answers it with a workload identity the application
  cannot forge or opt out of. A second in-app handshake inside the mesh tunnel
  would re-encrypt traffic that is already encrypted and still decide nothing.
- **It re-creates a certificate lifecycle nobody owns.** The removed listeners
  came with leaf reload, SNI handling and a private verification-key route,
  each its own failure mode, each duplicating something the mesh or the public
  key document already provides.
- **A named endpoint is not available here.** Binding a second endpoint to the
  same protobuf API needs the Codefly Go runtime to inject and bind multiple
  same-API endpoints (`P1-NET-007`). That is a gap in the runtime, and the fix
  belongs there — not a listener hand-rolled beside it.

## Three gates, and a call passes all of them

### 1. Reach — the mesh decides who may connect

The namespace runs `PeerAuthentication: STRICT`, so every source principal is
an authenticated mTLS workload identity. A generated `AuthorizationPolicy`
(`allow-accounts-internal-authority`) then ALLOWs the internal method paths —
enumerated from the `EXPOSURE_INTERNAL` methods in the service catalog — from
the SPIFFE principals of the services that **declare a dependency on accounts
in the workspace topology**, and from nobody else. Istio matches on the request
path, so this gates the internal surface by caller identity even while it stays
multiplexed on a shared port. The rendering is
`services/accounts/code/pkg/cataloggen/deployment_topology.go`, its golden is
`testdata/mesh-policy.golden.yaml`.

This module's own topology names one such caller, `auth-gateway`, because it
must carry no build-time knowledge of its consumers. **Reach for a composed
module is therefore the composition's to grant**: its workspace declares that
module's dependency on accounts and regenerates the policy, which adds that
service's ServiceAccount to the allowlist. A valid credential does not
substitute for it — the connection is refused before any token is read — and
neither does a network route: the policy is deny-by-default for every principal
it does not name, the ingress gateway's included.

### 2. Transport — h2c on the private REST endpoint

The `rest` endpoint's handler dispatches on the request: HTTP/2 with a
`content-type` of `application/grpc` goes to a separate internal gRPC server,
everything else to the REST mux (`pkg/adapters/rest_gen.go`). Both are served
by `h2c.NewHandler`, so there is no TLS in front of either.

The origin is **resolved, never configured as a URL**. A caller asks Codefly
for the endpoint and dials the host and port it returns with insecure transport
credentials — insecure meaning *this process adds no TLS*, not *unauthenticated*:
the mesh has already authenticated both ends. The reference implementation is
`services/auth-gateway/code/main.go`:

```go
net := codefly.For(ctx).Service("accounts").API("rest").NetworkInstance()
conn, err := grpc.NewClient(
    fmt.Sprintf("%s:%d", net.Hostname, net.Port),
    grpc.WithTransportCredentials(insecure.NewCredentials()),
)
```

Server reflection is not registered on the internal server, in any environment.
A caller cannot enumerate this surface; it generates its client from the
published descriptors.

### 3. Identity — two credentials that mean different things

`x-codefly-internal-token` (`CODEFLY_INTERNAL_TOKEN`, an `internal-auth`
configuration dependency) admits the internal tier. It is a perimeter
credential and says nothing about who is calling.

`x-codefly-work-context` carries the caller's signed Work Context, and the
calling principal and its bound tenant are taken from that verified capability
— never from request metadata. A module obtains one by presenting the identity
secret its composition provisioned; see
[services/accounts/AGENTS.md](./services/accounts/AGENTS.md) for the mint and
[WORK_CONTEXTS.md](./WORK_CONTEXTS.md) for the exchanges that attenuate one.

The two tiers do not overlap in either direction. The internal server refuses
any method whose policy tier is not internal; the tenant listeners refuse every
internal method even when an internal token is present
(`pkg/adapters/grpc_auth_interceptor.go`). An unclassified method is refused
everywhere.

## What a client library must do

- **Dial the resolved endpoint over h2c.** A library that requires an explicit
  `https://` origin, or that dials with channel credentials built from a CA
  bundle, cannot reach a current host. Requiring TLS here is not a safe
  default — it is an unsatisfiable one, and it hides the fact that the mesh is
  what is actually protecting the hop.
- **Send both credentials on every call**, and re-run the exchange rather than
  holding a capability open. The two lifetimes differ by more than an order of
  magnitude: a module identity context caps at fifteen minutes, an *exchanged*
  audience child at 60 seconds — less, when the parent expires sooner. A worker
  that caches an exchanged child for the identity context's lifetime is refused
  on every call after the first minute.
- **Never log either credential**, and never persist a Work Context.
- **Fail closed on refusal.** `PERMISSION_DENIED` from this tier means the
  perimeter credential, the tier, or the caller's declared authority did not
  admit the call; it is never a reason to retry against another transport.

## Verification keys

Work Contexts and access tokens are signed by the same Ed25519 key, and its
public half — with every retained verification key — is published as a standard
JSON Web Key Set at `GET /v1/auth/.well-known/jwks.json` on the same private
`rest` endpoint (`pkg/adapters/jwks_http.go`). **This document is the only
producer.** There is no private key route and no mountable key file.

A consumer fetches it in-cluster and verifies by `kid`, which is derived
deterministically from the public key, so two processes holding the same key
agree on its id with no coordination. Trust for the fetch is the same mesh
identity that gates everything else on this endpoint; the document carries no
secret and needs no pinned CA.

**Mounting a hand-authored copy is outside the contract.** A static file is a
key set frozen at the moment someone wrote it: the rotation runbook publishes
the incoming key and waits for every verifier to converge *before* signing with
it, and a mounted file converges never. A consumer that cannot re-read the
document at runtime breaks silently on the next rotation, and the first symptom
is every capability failing verification at once. The timing, the overlap
window and the observability of a rotation are in
[KEY_ROTATION.md](./KEY_ROTATION.md); a consumer's obligation is the cache
side of it — re-read on an unrecognised `kid`, bound the cache lifetime, and
fail closed rather than fall back to another key when the set expires.

## What this module does not export yet

The module interface publishes `accounts/connect` and `auth-gateway/grpc`.
Neither carries this tier: the tiering interceptor refuses internal methods on
both, and the brokered bootstrap routes live on the private `auth-gateway/rest`
endpoint. There is no module-visible endpoint name for the internal transport,
and the mixed private listener is deliberately not promoted to one — that is
`P1-NET-007` again, and until it lands the origin comes from the composed
workspace's own topology rather than from this module's published interface.

## Adopting a release that removed the TLS listeners

A composition upgrading onto a host that no longer serves the private TLS
listeners must sequence the consumers first. Before the upgrade:

1. Every consumer of the internal tier resolves a plaintext h2c origin, per
   *What a client library must do* above. A pinned image that still serves the
   removed listeners is what is hiding an unconverted consumer.
2. Every consumer that held a private custody client has moved to an installed
   binding: a `MODULE_PRINCIPALS` `operation_audiences` entry, redeemed with
   `ModuleCapabilitiesService.ExchangeDelegatedOperationAudience`. The library
   that used to carry that authority is gone from this repository, and the
   store migration that drops its table refuses to run while grants are live.
3. Every consumer that verifies host-signed credentials fetches the key set at
   runtime rather than mounting one.

Upgrading before all three hold does not fail at deploy time. It fails on the
first authority call, in whichever consumer was left behind.
