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

## Brokering a module's Work Context

`POST /modules/_work-context` mirrors the registration exchange: the
cluster-internal token plus the module's **identity** secret, body `{prefix}`.
The gateway brokers it to accounts
(`ModuleCapabilitiesService/MintModuleWorkContext`, `EXPOSURE_INTERNAL`), which
authorizes it against an independent digest and owns every rule about what the
resulting capability may do — see
[../accounts/AGENTS.md](../accounts/AGENTS.md). The gateway mints nothing itself
and adds no authority of its own.
