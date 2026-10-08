# AGENTS.md — auth-gateway

The single front door. Every request a consumer makes arrives here, gets an
ext_authz Check, and is proxied to a service — or to a declared solution
upstream from the durable registry. Read
[../../GATEWAY_ROUTES.md](../../GATEWAY_ROUTES.md) for the generated route
inventory and [../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md)
for the trust model behind declared presence.

Nothing here may name a specific solution or composed module: every seam is
generic, and a registered target is data, never a branch.

## What a deployed gateway refuses to start without

Three of this service's controls are only controls when the thing they depend on
is there, and each used to degrade into something that read as working:

- **A trusted-proxy range** (`gateway/TRUSTED_PROXY_CIDRS`). SP-GW-09 requires the
  anonymous and authentication-factor budgets to key on the originating client.
  Empty means no forwarding header is trusted, so `clientIP` answers the peer —
  behind the frontend, the frontend's own pod — and every caller shares one bucket.
  It read as working: the limiter was enabled and the buckets were enforced.
- **A shared rate-limit store.** Per-replica counters enforce the configured budget
  times the replica count, and which replica a caller lands on decides their share.
- **A revocation store.** A revoker with no store answers "not revoked" to every
  question, so a signed-out session, a killed device and an ended impersonation
  window all keep authenticating until their tokens expire — and no request can
  tell that from an empty revocation set.

Each refuses at boot outside local development, naming what to provision. Local
development keeps all three fallbacks: one replica, no ingress hop, no cache
service, and the alternative is a gateway that cannot start.

## Reading declared solution upstreams

`GET /solutions/_registry` returns this replica's read projection: identity,
publisher, revision, frontend manifest, declared target and status (`active`,
`pending`, `incompatible`, `tombstoned`). It never exposes the backend upstream.
The frontend reads it with the cluster-internal credential.

The gateway proxies `/solutions/{id}/…` using the same verified identity and
header discipline as catalog routes, with per-viewer installation admission.
Only public `/assets` and `/.well-known` paths accept unauthenticated GET/HEAD.

## Composed-module REST routes

A composed module is reached at `/v1/<alias>/*`, and the alias comes from the same
place a solution's does: the declared registry. The gateway holds **no
process-local module upstream registry** and nothing self-registers — main's
`/modules/_register`, where a module POSTed its own upstream and the gateway
believed it, is deleted.

The **catalog always wins**: `handleDeclaredModule` is consulted only after the
generated and explicit catalogs found no route, so a declared alias can never
shadow one. An alias nothing declares falls through to the ordinary 404, which is
what keeps the surface from being a probe for which modules a deployment runs. An
alias the catalog *owns* is refused outright rather than half-served — the matcher
takes only the paths it matches, so serving the rest would split one prefix
between two authorities.

Beyond that it is the **solution shape, with one deliberate difference**: the same
cache, the same single carried resolution, the same upstream URL policy, the same
guarded (resolve-revalidating) transport, the same tombstone rule, the same 120 s
revocation bound — and **no per-viewer installation admission**, because a module
is part of the composition rather than something an organisation installs. The
path is forwarded **unchanged** (a module owns its own `/v1/<alias>` surface),
where a solution's prefix is stripped.

That asymmetry is why the registry carries `SolutionDeclaredBinding.kind` and why
neither surface guesses: routing a solution here would put its upstream behind no
installation check. Each surface requires its own kind positively and answers a
403 verdict for the other — `module is not declared on this host`, or `solution is
not declared on this host`. `SOLUTION_DECLARED_KIND_UNSPECIFIED` is refused by
**both**: it is what a reader decodes from a writer that did not set the field, not
a third kind, so such a record is served by neither surface rather than by the one
needing less authority.

Module identity exchanges are described below; accounts owns their authority.

## A runtime-registered upstream never receives the person's session

The gateway removes `Authorization`, `Cookie` and `Proxy-Authorization` from every
request it forwards to a runtime-registered upstream — a federated module prefix or
a solution. A catalog route is untouched: accounts is the host's own API, where the
session IS the credential.

The host access token carries one host-wide audience and the person's full
authority, so it is not a credential any single upstream should hold. The cookie
goes the same way, and the gateway has already resolved identity from it by the time
it forwards, so an upstream reading it learns nothing it is not told.

An upstream receives the identity `ext_authz` stamped: the subject, the tenant, the
session, the credential kind and the scope ceiling. That names the person without
carrying their authority.

**SP-GW-07 has a second half this gateway does not yet satisfy.** The invariant is
that an upstream receives a host-minted context bound to THAT upstream's own
audience — not merely that it stops receiving the person's session. Today it
receives the stamped identity headers and no audience-bound context, so the
credential is no longer over-broad but the positive half is absent. Minting it at
the edge is open work here; it cannot be supplied by a consumer.

What is missing is specific, so that the next person does not have to rediscover it.
`WorkContextService` already exposes `ExchangeAudience`, which re-binds an existing
Work Context to a different audience — and that is not the operation this needs. The
caller here holds a browser SESSION, which is not a Work Context, so the edge would
need a mint that takes a verified session and returns a context bound to one
upstream's audience, with that person's authority narrowed to what the upstream may
do on their behalf. No RPC does that today, and adding one is a descriptor change
(contract digests, published clients, a verified regeneration), which is why the
stripping half shipped alone.

Two things to settle with it, because they are the reason it is not a small change:
the mint is per request on the proxy path, so its latency and its failure mode become
the failure mode of all federated traffic; and the authority to narrow TO is the
installation's ceiling, which the host cannot currently look up from a solution id
either (see `module/SOLUTION_REGISTRATION.md`).

**A consumer written against the old behaviour has to change**: anything that read
the forwarded bearer needs a host-minted capability bound to its own audience
instead.

## A registered client calls without a proxy

The host's own frontend reaches this gateway through a server-side proxy, so it
never needed cross-origin access and the gateway answered none: no `OPTIONS`, no
`access-control-allow-origin`. That made the proxy the only door. A second
client — an add-in, a CLI, a customer portal — could only get in by holding the
same shared secret, which is the arrangement the boundary rules forbid.

What opens the door is an identity, not a relaxation. accounts mints a client's
access token with `azp` naming its registration, the ext_authz check projects
that as `x-client-id` (stripped from caller input like every other canonical
identity header), and `ClientRegistryService/ListRegisteredClients` says which
exact browser origins that client declared. `code/gateway_clients.go` caches the
answer the way `gateway_solution_registry.go` caches the solution registry, and
`code/gateway_cors.go` decides from it:

- A **preflight** from a registered origin is answered before routing, from the
  origin alone — a preflight carries no credential, so approving one authorizes
  nothing. An origin the registry does not know falls through to the router and
  is refused, exactly as before. It never promises what the request path would
  withhold: a solution's public sub-paths run with identity stripped and no
  ext_authz check, so a bearer there names no client, and a preflight that
  intends to send one is refused rather than answered.
- The **request** is bound in `proxyTo`, the one point every forwarded request
  passes through whatever route matched it: a token naming a client must arrive
  from an origin that client registered, or it is refused before it reaches any
  upstream. A token issued to one client is therefore useless from another's
  page, on catalog, solution, and declared-module routes alike.
- `X-Codefly-Public-Origin` — the WebAuthn relying party, the origin an OAuth
  start is validated against — is derived from that registration. The frontend's
  internal-token path still establishes it too; the registration is the stronger
  claim, since it names the client rather than only the process that forwarded.

**The grant on a forwarded response is this gateway's, and only this gateway's.**
Every proxied response has its `access-control-*` headers stripped before the
gateway stamps whatever it granted — including when it granted nothing. An
upstream may answer CORS itself, and accounts does: its generated handler hands an
empty allowlist to a library that reads empty as allow-all, so it answers a
wildcard origin with credentials true. Forwarded verbatim, that made the public
surface grant a cross-origin read the gateway had granted nobody, and made an
empty allowlist grant everything. The generated handler is the `go-grpc` agent's
to fix; what the gateway owns is that nothing leaves it claiming a grant it did not
make.

Two deliberate limits. **Credentials are never allowed**: a registered client
authenticates with its bearer, and echoing `access-control-allow-credentials`
would additionally let a cross-origin page ride the host's session cookie.
And a request presenting a credential that names **no** client — the host's own
web session, an API key — is untouched by all of this. It takes the same path
with the same headers as before the registry existed, which matters because the
frontend's proxy copies the browser's `Origin` header through verbatim, so a
host page's own same-site write arrives here carrying an origin the registry has
never heard of. A cookie is treated as such a credential: a cross-origin page
cannot attach one, so a request bearing one came through that proxy.

A request carrying **no** credential at all is the one case judged on its origin
alone, because obtaining the first token is itself a cross-origin call made
before there is any `azp` to bind to. Judging it on the bearer would leave a
registered client able to sign in and never able to read the token it signed in
for — the exchange would be served, the code spent, and the browser would
discard the response.

Consequently the solution **public** surface (`/assets`, `/.well-known`) is
granted on its origin alone, like any other uncredentialed read — a module
loader pulling a remote's chunks is exactly that. What it cannot do is carry a
bearer: identity is stripped there and no ext_authz check runs, so the gateway
has nothing to bind, and both halves say no rather than one promising what the
other drops.

## Brokering a module's Work Context

`POST /modules/_work-context` takes the
cluster-internal token plus the module's **identity** secret, body `{prefix}`.
The gateway brokers it to accounts
(`ModuleCapabilitiesService/MintModuleWorkContext`, `EXPOSURE_INTERNAL`), which
authorizes it against an independent digest and owns every rule about what the
resulting capability may do — see
[../accounts/AGENTS.md](../accounts/AGENTS.md). The gateway mints nothing itself
and adds no authority of its own.

`POST /modules/_operation-context` is its headless sibling, on the same
perimeter: the cluster-internal token in `X-Codefly-Internal-Token`, the module's
**identity** secret in `X-Codefly-Module-Secret`, and body
`{"prefix": "<module>", "binding": "<operation_audiences key>"}`. The gateway
brokers it to `ModuleCapabilitiesService/MintModuleOperationContext` and answers
`{work_context, expires_at, principal_id, tenant, audience, binding}` (RFC 3339
`expires_at`) with `cache-control: no-store` — snake_case, unlike the camelCase
`/modules/_work-context`, because consumers were built against this shape. It relays two distinct refusals: `401` when the module is not proven
(bad internal token, missing or wrong secret, undeclared module) and `403` when a
proven module names a binding it may not mint with no person present. What the
binding yields — audience, scopes, lifetime — is accounts' decision alone.

`POST /modules/_source-operation-context` is the source-delegation sibling, on
the same perimeter and headers, with body `{"prefix": "<module>", "source_id":
"<uuid>"}` or `{"prefix": "<module>", "delegation_id": "<uuid>"}` (exactly one).
It brokers to `ModuleCapabilitiesService/MintSourceOperationContext` and answers
`{work_context, expires_at, principal_id, owner_principal_id, tenant, audience,
binding, delegation_id, source_id}` with `cache-control: no-store`. Three
refusals stay distinct: `401` for an unproven module, `412` with body
`DELEGATION_MISSING` when the source has no active delegation to the module (a
person must reconnect it — read from accounts' `google.rpc.ErrorInfo`, never
from the message), and `403` when the delegation named is revoked or not usable
by the module — body `DELEGATION_REVOKED` or `DELEGATION_INVALID`, read from the
same `ErrorInfo`. Only those two reasons are relayed; any other denial is a bare
`403` with body `forbidden`, so the gateway never says more about a delegation
than accounts decided to. The design is in
[../../WORK_CONTEXTS.md](../../WORK_CONTEXTS.md#operation-contexts-from-a-source-delegation).

These three routes are the wire contract of the published module-authority
client (`saas-sdk-go/moduleauthority`), which mints here and exchanges on
accounts' `authority` endpoint — never with a Connect call to this gateway,
which answers 404 for every internal procedure. In the canonical repository,
`qualification/module-authority` drives that client against this gateway's real
handler and accounts' real internal tier and `authority` endpoint, each run as a
helper test from its own module (`TestModuleAuthorityContractGateway` here,
`TestModuleAuthorityContractHost` in accounts). Change a path, header, JSON
field, status or refusal body here and that qualification is what fails.
