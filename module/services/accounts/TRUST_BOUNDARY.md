# Accounts trust boundary

The authenticated product has one public entry: `frontend/http`. The frontend
same-origin proxy reaches the private `auth-gateway/rest` gateway, which then
reaches private Accounts REST/Connect endpoints. Accounts transports and the
auth-gateway HTTP endpoint are not publicly exported by `module.codefly.yaml`.

## Listener contract

| Listener | Callers | Admitted RPCs | Credential |
|---|---|---|---|
| `grpc` | Private module services and diagnostics | Public and tenant RPCs | JWT, or gateway-stamped identity with `CODEFLY_GATEWAY_TOKEN` |
| `rest` | `auth-gateway` | Public and tenant HTTP routes; internal gRPC multiplexed over h2c | REST uses tenant policy; gRPC admits internal-tier RPCs only with `CODEFLY_INTERNAL_TOKEN` |
| `connect` | `auth-gateway` | Public and tenant Connect RPCs | JWT, or gateway-stamped identity with `CODEFLY_GATEWAY_TOKEN` |

Tenant gRPC, Connect, and the REST-to-gRPC backend reject internal-tier RPCs
even when an internal token is present. On the private REST listener, HTTP/2
`application/grpc` traffic is dispatched to a separate internal gRPC server
which rejects every non-internal RPC. Unknown methods fail closed everywhere.

## Internal-authority reach gate (mesh)

Reach and identity are split. Even though the internal RPCs stay multiplexed on
the shared HTTP port, the mesh gates *reach* by caller workload identity: an
Istio `AuthorizationPolicy` (`allow-accounts-internal-authority`, generated from
the `EXPOSURE_INTERNAL` methods in the service catalog) **allows the internal
method paths only from the service accounts of the callers the workspace
topology declares**, so every other source principal — the ingress gateway's
included — is denied by default. Combined with namespace
`PeerAuthentication: STRICT`, a request to an internal method path from a
principal the policy does not name is rejected at the mesh before the handler.
Istio matches by request path, so this holds without a dedicated port.

mTLS authenticates the *workload*, not the end user or tenant: it is the reach
gate only. `requireInternalCredential` remains the app-layer *identity* gate
(internal vs tenant), and per-RPC user/tenant checks stay necessary.

The mixed private listener is an in-module implementation detail, not a product
integration endpoint. It is intentionally absent from the module interface.
Cross-module installed product services depend on the named `authority`
endpoint instead (`P1-NET-007`), which serves only the module surface; the
public auth-gateway never exposes internal methods such as `ConsumeUsage`. The
outward-facing half of this
listener — what a composed module's client must resolve and must not require —
is [../../INTERNAL_TRANSPORT.md](../../INTERNAL_TRANSPORT.md).

## Forwarded identity

The frontend removes caller-supplied origin trust headers, stamps the actual
browser origin with `CODEFLY_INTERNAL_TOKEN`, and forwards only API routes to
auth-gateway. The auth-gateway ext_authz check accepts that origin only after constant-time token
validation.

The gateway removes all caller-supplied identity, organization, role, scope,
MFA/assurance, gateway-token, and internal-token headers before authorization,
and every `Grpc-Metadata-*` header with them: the REST transcoder in front of
Accounts would otherwise forward `Grpc-Metadata-X-User-Id` as `x-user-id`.
After a successful JWT or API-key check, auth-gateway emits canonical identity
headers and signed authentication evidence (`amr`, `auth_time`, `acr`, and the
last MFA verification time), and stamps `X-Codefly-Gateway-Token` itself on
Accounts routes only. The ext_authz check's answer never carries the gateway
token, because the gateway's gRPC listener returns that answer to any caller;
an Envoy in front of Accounts must stamp the token itself, and the generator in
`auth-gateway/code/envoy.go` does not.

A caller's `Connection` header cannot remove anything the gateway stamps. The
gateway's reverse proxy deletes every header `Connection` names (RFC 9110
§7.6.1) after the identity headers and the gateway token are on the request;
left to it, `Connection: X-Scopes` would deliver an API key's identity without
its scope list. So the gateway applies `Connection` itself first: the caller's
own named headers are still dropped, a header only the gateway may set never
is, and the forwarded `Connection` names nothing but `Upgrade`.

Accounts accepts those identity headers only when exactly one gateway token is
presented and it matches in constant time, and only under their own names: the
REST transcoder never turns a `Grpc-Metadata-`-prefixed header into metadata,
and a trusted request that names an identity field or the public origin twice
is refused rather than resolved, on the Connect, gRPC and plain-HTTP
transports alike. A trusted request must also carry both headers the gateway
stamps on every request it admits: `X-Credential-Kind`, which is `session` or
`api_key`, and `X-Scopes`, which is empty for a session and for a key created
without scopes. One that arrives without either, or with another kind, lost
part of the assertion on the way and is refused: without the kind accounts
cannot tell a key from a session, and without the scope list it cannot bound a
key. Without the token, the headers are removed and accounts verifies the
Bearer JWT itself. `CODEFLY_GATEWAY_TOKEN` is deliberately different from
`CODEFLY_INTERNAL_TOKEN`: proving identity-header provenance never grants
access to internal RPCs.

## API-key scopes

An API key's authority is its scope list, checked against the scopes the RPC
declares in its method policy (`saas.policy.v1.method_policy.scopes`). The
Connect and gRPC interceptors check it on every RPC, before any handler, so it
covers the REST routes too, which reach accounts through the Connect listener:

- an RPC that declares no scope has not been opened to API keys and refuses
  every key, whatever the key carries;
- a key is admitted to an RPC when one of its scopes covers one the RPC
  declares, with `*` matching either segment (`invitations:*`, `*:write`,
  `*:*`);
- a key with no scopes has no authority anywhere.

The plain-HTTP routes behind billing and the subscription stream are not RPCs
and declare no scope, so they refuse API keys with `403`.

**An API key cannot create API keys.** Only an interactive session can:
`CreateAPIKey` declares no scope, so admission refuses every key, and the
handler refuses any caller the perimeter did not report as a session before it
checks anything else. A key that could mint would copy itself with no expiry —
accounts cannot see the calling key's own — so a leaked expiring key would
become a permanent one. `api_keys:write` on a key reaches `RevokeAPIKey` only.
Automation that needs a new key gets it from a signed-in session.

A session that presents a scope list is held to it when it mints: `CreateAPIKey`
grants only scopes that list covers. Each half of a scope is one name (letters,
digits, `.`, `_`, `-`, or a lone `*`): a key's scopes reach every service joined
into one comma-separated `X-Scopes`, so a scope holding `:`, `,` or whitespace is
refused when minted and when a stored key is validated, and the gateway refuses
one holding `,` rather than join it — otherwise it reads back as other scopes
(`users` / `read,*:*` as the root `*:*`). A session is otherwise unaffected by
all of this: its authority is RBAC, checked by the handlers and the central
tenant floor.

## Where the tokens live

Both values are secret Codefly workspace configuration, and a group is
delivered whole to every service that declares it:

| Value | Group | Declared by |
|---|---|---|
| `CODEFLY_INTERNAL_TOKEN` | `internal-auth` | frontend, accounts, auth-gateway |
| `CODEFLY_GATEWAY_TOKEN` (and, while rotating, `CODEFLY_GATEWAY_TOKEN_PREVIOUS` with `CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT`) | `gateway-trust` | accounts, auth-gateway |

The gateway token has a group of its own because whoever holds it can assert
any identity to accounts, and the frontend declares `internal-auth`. The
repository-root test `TestGatewayTokenReachesOnlyAccountsAndTheGateway` fails
if any other service declares a group that carries it. Production environments
must supply independent, high-entropy values.

### Rotating the gateway token

Accounts accepts a previous gateway token beside the current one, comparing
both in constant time, until `CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT`, an
RFC 3339 timestamp such as `2026-03-01T18:00:00Z`; auth-gateway only ever
stamps the current one. At the expiry the old value stops proving anything
whether or not anyone has cleared the slot. Accounts refuses to start while a
previous token is set with an expiry that is missing, unreadable, already
past, or more than 24 hours ahead, and when the previous token equals the
current one. An expiry with no previous token is ignored. Rotate in this order,
letting each step converge before the next:

1. **accounts:** set the new value as `CODEFLY_GATEWAY_TOKEN`, the old one as
   `CODEFLY_GATEWAY_TOKEN_PREVIOUS`, and `CODEFLY_GATEWAY_TOKEN_PREVIOUS_EXPIRES_AT`
   to the time by which step 2 will have converged, and roll accounts. Both
   are accepted until the expiry.
2. **auth-gateway:** set the new value as `CODEFLY_GATEWAY_TOKEN` and roll the
   gateway. Pods still stamping the old value are accepted through the
   previous slot until they are replaced or the expiry passes.
3. **accounts:** clear `CODEFLY_GATEWAY_TOKEN_PREVIOUS` and its expiry, and
   roll accounts. The old value no longer proves anything. Do this before the
   expiry: an accounts pod that restarts after it with the slot still set
   refuses to start.

Keep the overlap short: until the expiry, the old value is as good as the new
one. When the old value may have been exposed, go straight through all three
steps.

On the first rollout of the release that introduced this expiry, issue a
brand-new gateway token and do not carry the pre-existing one into either
slot: before the `gateway-trust` group existed, the gateway token travelled in
`internal-auth`, which is delivered to the frontend, so it must be treated as
exposed. Set the new value on accounts and auth-gateway together; until a
gateway pod is replaced, the identity it forwards is not trusted.

## Forwarded client addresses

The public gateway trusts `X-Forwarded-For` and `X-Real-IP` only when the TCP
peer belongs to `TRUSTED_PROXY_CIDRS`, supplied through Codefly's `gateway`
workspace configuration. With an empty allowlist, forwarding headers are
ignored. Trusted chains are parsed as IP addresses and walked right-to-left;
the first untrusted hop is the client. Malformed chains and chains longer than
32 hops fall back to the TCP peer.

Accounts never uses `X-Forwarded-Proto` to weaken cookie attributes. Refresh
cookies are always `Secure`, including local development.

## Gateway rate-limit storage

Auth-gateway resolves the Codefly `cache/write` endpoint directly. `REDIS_URL`
can override it for hosted Redis and supports both `redis://` and `rediss://`
URLs, authentication, and database selection. The client uses a bounded pool,
connection/read/write/pool timeouts, TLS 1.2 or newer for `rediss://`, and an
atomic script that assigns expiry only when a counter is created. Connection
errors never log the configured URL or credentials.

Limiter dependency failure is explicit by route class. Authentication,
refresh, and MFA routes fail closed with `503` and `Retry-After`; accepting
those requests without the limiter would reopen brute-force paths. Normal
authenticated application traffic and inbound delivery webhooks fail open to
preserve availability, while service authorization and webhook verification
continue to apply.

MFA login completion also has an independent Codefly-configured attempt budget
per trusted client IP (10/minute locally). The database separately locks each
one-use MFA transaction after five rejected factors, so changing gateway
replicas, protocols, ports, or spoofed tenant headers cannot reset the factor
budget. Unknown, expired, consumed, invalid, and locked transactions return
the same public rejection.

## Gateway probes

`/health` is process-only liveness and never depends on Redis or Accounts.
`/ready` is generated from the exact API route catalog: every referenced
upstream must be configured and accept a TCP connection. Missing, malformed,
or partially unavailable upstream sets return `503`, so orchestration cannot
send traffic to a partially assembled gateway.

## Route generation

Connect routes are generated from protobuf descriptors joined with the shared
RPC policy inventory. Internal-tier methods are excluded during discovery.
REST routes remain opt-in; internal permission-check routing is explicitly
disabled. Envoy forwards only the canonical auth response headers and strips
untrusted auth headers on public routes where `ext_authz` is disabled.
