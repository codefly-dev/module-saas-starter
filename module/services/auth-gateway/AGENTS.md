# AGENTS.md — auth-gateway

The single front door. Every request a consumer makes arrives here, gets an
ext_authz Check, and is proxied to a service — or to a **runtime-registered**
upstream it learned about with no rebuild. Read
[../../GATEWAY_ROUTES.md](../../GATEWAY_ROUTES.md) for the generated route
inventory and [../../SOLUTION_REGISTRATION.md](../../SOLUTION_REGISTRATION.md)
for the trust model behind the registration surfaces below.

Nothing here may name a specific solution or composed module: every seam is
generic, and a registered target is data, never a branch.

## Solution upstream registration

`POST /solutions/_register` (`code/gateway_solutions.go`) takes `{id, upstream}`;
**the gateway supplies the compare-and-swap revision it last saw**, not the
registrant. `GET /solutions/_registry` returns this replica's snapshot — id,
publisher, revision, and status (`active` / `pending` / `expired` /
`incompatible` / `tombstoned`), **never an upstream** — which is both what the
frontend rebuilds from and what an operator reads to tell those states apart.

The gateway then proxies `/solutions/{id}/…` to the registered upstream, running
**the same ext_authz Check and identity-header discipline as catalog routes**.
Only the public `/assets` and `/.well-known` sub-paths are served
unauthenticated, and only for GET/HEAD.

## Both registration halves take an owner-bound credential

Neither the gateway half nor the frontend half is gated on the shared
cluster-internal token. The caller presents a signed, solution-bound
registration token in `X-Codefly-Solution-Registration`, obtained from `POST
/solutions/_registration-token` against the solution's own secret
(`SOLUTION_REGISTRATION_SECRETS` in the `federation` group, declared separately
from the module secrets). The credential names one solution id and one
publisher, so a holder can neither claim nor re-point another solution.

## Composed-module REST federation

A composed module that serves its own `/v1/<module>/*` surface registers it with
`POST /modules/_register` (`code/gateway_modules.go`), and the gateway proxies
that prefix **once the generated catalog has no match**. Unlike solution
registration this is **not** gated on the shared internal token: the caller must
present a signed, prefix-bound registration token in
`X-Codefly-Module-Registration`, so a module holding the credential for one
prefix cannot claim another. The handshake is three calls:

1. `POST /modules/_registration-token` on the auth-gateway, with the
   cluster-internal token in `X-Codefly-Internal-Token` **and** the module's own
   registration secret in `X-Codefly-Module-Secret`, body `{prefix}`.
2. The gateway brokers to accounts over the internal listener
   (`ModuleCapabilitiesService/MintModuleRegistration`, `EXPOSURE_INTERNAL`, so
   the generated mesh policy admits the gateway's service account and denies
   everyone else). accounts compares the secret against the digest declared for
   that prefix in the `federation` configuration group's
   `MODULE_REGISTRATION_SECRETS` and, on a match, mints a 5-minute Ed25519 token
   (`aud=module-registration`, `sub=module:<prefix>`) with the key the gateway
   already trusts through JWKS, emitting a `module.registration_minted` audit
   event. **Unset means no module may federate.**
3. `POST /modules/_register` with that token and `{prefix, upstream}`.

Composition provisions the pair: the SHA-256 digest into this host's `federation`
group, the plaintext into the module. The token is short-lived and fetched **per
registration attempt**, not cached across a gateway restart. Registration only
adds a proxy target — every proxied `/v1/<module>/*` request still runs the full
ext_authz check.

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
  page, on catalog, solution, and federated-module routes alike.
- `X-Codefly-Public-Origin` — the WebAuthn relying party, the origin an OAuth
  start is validated against — is derived from that registration. The frontend's
  internal-token path still establishes it too; the registration is the stronger
  claim, since it names the client rather than only the process that forwarded.

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

`POST /modules/_work-context` mirrors the registration exchange: the
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
