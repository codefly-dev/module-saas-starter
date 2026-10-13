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

It uses the **solution shape**: the same cache, single carried resolution,
upstream URL policy, guarded transport, tombstone rule and 120 s presence
revocation bound. Before forwarding a bearer it asks accounts for the viewer's
live entitlement to the resolved target, without caching that decision. Accounts
intersects the delivered authority, envelope and organization's installation
scope. The path is forwarded unchanged; a module owns its `/v1/<alias>` surface.

The registry's `SolutionDeclaredBinding.kind` positively identifies each surface.
An unspecified kind is refused by both. Neither presence nor a reusable alias
constitutes organization consent.

Module identity exchanges are described below; accounts owns their authority.

## What a runtime-registered upstream receives, and what it does not

A runtime-registered upstream — a federated module prefix or a solution — receives
the person's `Authorization` bearer and does NOT receive their `Cookie` or
`Proxy-Authorization`. A catalog route is untouched: accounts is the host's own API,
where the session IS the credential.

That split is a decision, not a halfway implementation, and it replaces an earlier
rule here that said all three were removed from every such request. Each half has
its own reason:

**The cookie never travels, whatever the bearer does.** Nothing upstream can
authenticate with it — this perimeter refuses a cookie AS a credential by name (see
`ExtAuthz.Check`) — and it carries the longest-lived value a browser holds, the
refresh credential. Forwarding it delivers that value to a third party for no
purpose it could serve. The two removals used to be one decision, and the broader
one silently carried the narrower: a request on the MCP tool path skipped the whole
removal and forwarded `Cookie` with it.

**The bearer does travel, to a DECLARED upstream, when the request is
authenticated.** A declared upstream authenticates the request by that header; the
runtime's HTTP wrapper and its passthrough handler both answer 401 without it. The
capability that should replace it — a host-minted context bound to that upstream's
own audience — is minted by nothing here and verified by nothing on the consumer
side, and the consumer half lives in another repository. Removing the header first
would break every composed solution while shipping a credential no consumer reads.

**It does not travel on the anonymous module-federation surface**, which runs no
ext_authz at all: a bearer arriving there was never authenticated for that upstream,
and the loader fetches those bytes without a person.

An upstream also receives the identity `ext_authz` stamped: the subject, the tenant,
the session, the credential kind and the scope ceiling. That names the person
independently of the bearer beside it.

**SP-GW-07's second half is open, recorded as `SA-F-BEARER`.** The invariant is that
an upstream receives a host-minted context bound to THAT upstream's own audience —
not merely that it stops receiving the person's session. The host access token
carries one host-wide audience and the person's full authority, so a declared
upstream holding it holds more than it needs, and that exposure is real while this
stays open. What closes it: a host-minted context bound to the upstream's own
audience, plus consumer verification of it, plus one composed browser-to-consumer
request exercised end to end.

What is missing is specific, so that the next person does not have to rediscover it.
`WorkContextService` already exposes `ExchangeAudience`, which re-binds an existing
Work Context to a different audience — and that is not the operation this needs. The
caller here holds a browser SESSION, which is not a Work Context, so the edge would
need a mint that takes a verified session and returns a context bound to one
upstream's audience, with that person's authority narrowed to what the upstream may
do on their behalf. No RPC does that today, and adding one is a descriptor change
(contract digests, published clients, a verified regeneration).

Two things to settle with it, because they are the reason it is not a small change:
the mint is per request on the proxy path, so its latency and its failure mode become
the failure mode of all federated traffic; and the authority to narrow TO is the
installation's ceiling. That second one is no longer unreachable — the entitlement
authority answers it on the path that resolves consent, and this gateway now states
it as `x-codefly-installation-id` (see below) — but it is answered for one request
at a time, not as a lookup from a solution id (see
`module/SOLUTION_REGISTRATION.md`).

**A consumer written against the old behaviour does NOT have to change for the
bearer.** Two things do change at the edge: a declared upstream no longer receives
the caller's cookie, and a resource-bound token is confined — it reaches the three
places below and nowhere else. Anything replaying such a token elsewhere stops
working, deliberately.

## Three places for a resource-confined credential

A token issued for a solution's MCP endpoint (RFC 8707 resource indicator) is held
by a third-party client and, once forwarded, by the runtime it dialled. It reaches:

- that solution's own routes, where the resource and the path agree;
- `/saas.accounts.v1.WorkContextService/StartTask`, the delegation mint; and
- `/v1/<alias>/*` for a DECLARED MODULE, and only in the company of a verified
  capability for that module minted on this person's behalf.

The second and third exist because the consumer's own work needs them, and they are
one flow, not two. The runtime's MCP viewer path derives a module gateway by minting
a viewer-owned Work Context — carrying the person's bearer, because its own service
credential does not stand in for the person — and then calls the module with that
capability AND the same bearer, since a declared upstream still authenticates by the
bearer (`SA-F-BEARER`, above). Admitting the mint alone moved the failure one request
later: the tool minted and was then refused at the module.

**Admitting the bearer at module routes on its own would have been the confinement
deleted, not relocated** — a one-solution credential with the person's whole
authority at every declared module. So the pairing is the admission:
`gateway_module_capability.go` requires a capability for THAT module, in the
bearer's own organization, minted in its own session. The session binding is what
makes "minted under the consumer's ceiling" a fact rather than an inference: a
capability for the same module could otherwise have come from the same person's
ordinary browser session, where no consumer ceiling applies. It is also sufficient
to name the owner, because accounts signs into a capability only its caller's
verified session or a fresh one — never a session a request names — and a session
belongs to one person; so the owner is not read here, which keeps the edge to what
core's `Inspector` exposes to a forwarding hop. Dropping the owner comparison is
deliberate, not an omission: the session already names the owner, and a second
check on a property the session establishes would be a check the edge cannot make
from what it holds. `TestModuleRoute_TheSessionIsTheBindingAndTheOwnerIsNotRead`
pins the dependency, so a change to how accounts assigns sessions is caught there.

**The edge check is core's `Inspector`** (`work_context.go`): encoding, issuer,
the signature under the key the capability names, window, shape, and the audience
of the route this hop resolved. It reads no live state and consumes nothing; the
callee verifies at full strength. A catalog route resolves no audience of its own,
so there the audience is left to the callee — exactly the strength that route had
before — and the code says so rather than hiding it behind an empty sentinel.

**The ceiling itself is accounts', not this gateway's.** `ext_authz` stamps
`x-codefly-resource`; the gateway resolves the solution that resource names, requires
the organization to have installed it and the viewer to hold a grant reaching it —
the same question, through the same authority, as every proxied request to that
solution — and states WHICH installation admitted the request as
`x-codefly-installation-id`. Accounts then refuses a mint for another organization,
for an audience the consumer does not declare it consumes, or for scopes outside
what it declares there. An authority that cannot be asked answers 503; a capability
is not issued on an unanswered question.

**The ceiling is the consumer's delivered authority, not a list an administrator
keeps.** Accounts reads the registry grant of the principal the stated installation
records (`business.ConsumerInvokeCeiling`): the invoke half of its operation
audiences, derived from the consumer's signed authority document, the applied
presence and the envelope, and intersected with the installation's consent. The
audience must be one of those bindings' audiences, and the scopes must fit one binding
there. The installation's `allowed_audiences` and `allowed_scopes` then narrow when
they name something and add no bound when empty: they are never a source of
authority, so lists that are empty or wider than the grant admit nothing it does not.

**What reaches that grant today.** The renderer derives an authority document from a
module's `module.contract.codefly.yaml`, and from nothing else. A binding there may
declare `invoke` alone — lookup is optional, and an invoke-only binding is kept — so a
module that publishes a contract reaches this ceiling. **A solution's `api.consumes`
in `solution.codefly.yaml` does not:** it feeds the generated SDK and
`CODEFLY__API_CONSUMES`, never an authority document. A solution with no contract has
no registry grant, so every confined mint addressed through it is refused. Rendering a
solution's declared consumption into its authority document is the delivery half of
issue #1083 and belongs to the renderer.

What a refusal says, and who acts on it:

- **Not consented.** Consent is fixed when an organization installs (the full grant at
  that moment). A module the consumer declares afterwards, or one an administrator
  narrowed away, is refused as not consented by this installation; installing the
  consumer again consents to what it declares now. Consent cannot be widened in place.
- **Not declared**, or scopes outside what is declared at that audience: the
  consumer's delivery has to declare it.
- **The consumer's own audience** (its prefix, or `solution:<its binding>`): refused,
  because the credential already reaches that consumer's own routes. It is refused
  before any binding is read: a self-admitting binding (headless or delegation work)
  may name its own module, and it must never admit a person's credential held by a
  third party.
- **Capabilities withdrawn** by the installation's consent: refused as such, not as an
  inactive installation; installing again restores them.
- **An installation that records no delivered authority** (made before installations
  recorded it) declares nothing. This gateway's entitlement check already leaves such
  an installation out, so the caller sees the generic 403 above before any mint; the
  named refusal in accounts is the second line, not what an operator will read.

The decision holds until the capability leaves: StartTask opens the authority use
span before any other transaction, so an uninstall or narrowed consent waits for the
mint. The span does not hold the in-memory registry a redelivery replaces, so the
consumer's declaration is re-checked after signing and a changed one refuses the
capability (`Aborted`, ask again). A withdrawal published after that check lands after
issuance, bounded by the capability's lifetime like every other one.

Both stamps are on `untrustedAuthHeaders`, so a caller can assert neither and cannot
pick its own ceiling. The consumer check and the installation stamp run on the HTTP
gateway path, where the registry and the entitlement authority live; a deployment
driving `ext_authz` from Envoy gets the route confinement and the carried resource
but neither the consumer check nor the module pairing — the same asymmetry the
gateway credential already has, and in the same direction: Envoy mode refuses the
module route rather than admitting it unpaired.

`x-codefly-installation-id` is also what a solution's runtime reads to name an
installation on every mint it makes. It will not take one out of a capability a
caller carried — inbound, that carrier is caller-controlled — so the hop that
resolved the consent is the only party that can supply it.

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
  start is validated against — is NOT derived from that registration, and was.
  A registered client's own origin was stamped as the verified public origin,
  which made the host's relying party and its sign-in redirect target a value a
  client chose by registering it; accounts' readers all compare the forwarded
  origin against the configured one, so the two authorities disagreed and the
  weaker one was the one that named the client. The configured origin is the
  authority, and the frontend's internal-token path is what establishes it.
  A registered client therefore does NOT get a verified origin of its own: a
  WebAuthn ceremony and a `BeginOAuth` redirect for such a client resolve against
  the configured origin or the static allowlist, which is also the only reading
  compatible with one relying-party id per deployment. Cross-origin access for
  the client is unaffected — that is the CORS decision above, from the same
  registration.

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
cluster-internal token plus the module pod's **projected ServiceAccount token**
as `Authorization: Bearer <token>` (exactly one header, that scheme), body
`{prefix, organization_id?}`. A shared module selects an organization; accounts
requires its live installation and the envelope's `cross_tenant` grant. A
tenant-pinned module may omit the selector or name only its pinned tenant.
No secret header is read: `X-Codefly-Module-Secret` is deleted with
the secret (#1056) and only stays on the strip list. The gateway forwards the
token unread to accounts (`ModuleCapabilitiesService/MintModuleWorkContext`,
`EXPOSURE_INTERNAL`), which reviews it, binds it to the pod's running build
(`BindExecution`) and owns every rule about what the resulting capability may
do — see
[../accounts/AGENTS.md](../accounts/AGENTS.md). The gateway mints nothing itself
and adds no authority of its own.

`POST /modules/_operation-context` is its headless sibling, on the same
perimeter: the cluster-internal token in `X-Codefly-Internal-Token`, the module
pod's projected token as `Authorization: Bearer`, and body
`{"prefix": "<module>", "binding": "<operation_audiences key>", "organization_id": "<org UUID>"}`
(the organization selector may be omitted for a tenant-pinned module). The gateway
brokers it to `ModuleCapabilitiesService/MintModuleOperationContext` and answers
`{work_context, expires_at, principal_id, tenant, audience, binding}` (RFC 3339
`expires_at`) with `cache-control: no-store` — snake_case, unlike the camelCase
`/modules/_work-context`, because consumers were built against this shape. It relays two distinct refusals: `401` when the module is not proven
(bad internal token, a missing or refused projected token, another workload, an
unapproved build, an undeclared module) and `403` when a proven module names a
binding it may not mint with no person present. Every module exchange answers
`503` when accounts could not establish what the pod is running (`Unavailable`):
nothing was decided, and the module retries. What the
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
