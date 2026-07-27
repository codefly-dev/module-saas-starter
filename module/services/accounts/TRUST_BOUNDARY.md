# Accounts trust boundary

The module has one public ingress: `auth-sidecar/rest`. The accounts REST,
Connect, tenant gRPC, internal gRPC, and frontend endpoints stay private to the
Codefly module and are not exported by `module.codefly.yaml`.

## Listener contract

| Listener | Callers | Admitted RPCs | Credential |
|---|---|---|---|
| `grpc` | Private module services and diagnostics | Public and tenant RPCs | JWT, or gateway-stamped identity with `CODEFLY_GATEWAY_TOKEN` |
| `rest` | `auth-sidecar` | Public and tenant HTTP routes; internal gRPC multiplexed over h2c | REST uses tenant policy; gRPC admits internal-tier RPCs only with `CODEFLY_INTERNAL_TOKEN` |
| `connect` | `auth-sidecar` | Public and tenant Connect RPCs | JWT, or gateway-stamped identity with `CODEFLY_GATEWAY_TOKEN` |

Tenant gRPC, Connect, and the REST-to-gRPC backend reject internal-tier RPCs
even when an internal token is present. On the private REST listener, HTTP/2
`application/grpc` traffic is dispatched to a separate internal gRPC server
which rejects every non-internal RPC. Unknown methods fail closed everywhere.

The mixed private listener is a transitional in-module implementation detail,
not a product integration endpoint. It is intentionally absent from the module
interface. Cross-module installed product services must wait for the generated
named internal gRPC endpoint in `P1-NET-007`; the public auth-sidecar never
exposes internal methods such as `ConsumeUsage`.

## Forwarded identity

The gateway removes all caller-supplied identity, organization, role, scope,
MFA/assurance, gateway-token, and internal-token headers before authorization.
After a successful JWT or API-key check, auth-sidecar emits canonical identity
headers, signed authentication evidence (`amr`, `auth_time`, `acr`, and the
last MFA verification time), and `X-Codefly-Gateway-Token`.

Accounts accepts those identity headers only when the gateway token matches in
constant time. Without it, the headers are removed and accounts verifies the
Bearer JWT itself. `CODEFLY_GATEWAY_TOKEN` is deliberately different from
`CODEFLY_INTERNAL_TOKEN`: proving identity-header provenance never grants
access to internal RPCs.

Both values are secret Codefly workspace configuration dependencies shared by
accounts and auth-sidecar. Production environments must supply independent,
high-entropy values and rotate them together across both services.

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

Auth-sidecar resolves the Codefly `cache/write` endpoint directly. `REDIS_URL`
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

`/health` is process-only liveness and never depends on Redis, accounts, or the
frontend. `/ready` is generated from the exact route catalog: every referenced
upstream must be configured and accept a TCP connection. Missing, malformed,
or partially unavailable upstream sets return `503`, so orchestration cannot
send traffic to a partially assembled gateway.

## Route generation

Connect routes are generated from protobuf descriptors joined with the shared
RPC policy inventory. Internal-tier methods are excluded during discovery.
REST routes remain opt-in; internal permission-check routing is explicitly
disabled. Envoy forwards only the canonical auth response headers and strips
untrusted auth headers on public routes where `ext_authz` is disabled.
