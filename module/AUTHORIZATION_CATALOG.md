# Generated authorization catalog

Status: generated and runtime-active at the edge (`saas.authz.methods.v1`).

The authorization catalog is the method-policy projection of the normalized
service catalog. It gives policy decision points, transport adapters, audit
tools, and CI one typed input without making them understand gateway routes or
walk live protobuf descriptors independently.

## Artifacts

| Path | Role |
| --- | --- |
| `services/accounts/proto/saas/catalog/v1/catalog.proto` | Typed `AuthorizationCatalog` and `AuthorizationMethod` schema. |
| `services/accounts/generated/authz-methods.json` | Complete machine-readable PDP inventory for all 121 methods. |
| `services/accounts/code/pkg/cataloggen/authz_methods.go` | Fail-closed compiler, policy validator, and deterministic renderer. |
| `services/auth-gateway/code/authz_catalog_gen.go` | Generated edge-safe policy lookup and REST-to-procedure joins. |
| `services/accounts/AUTHZ_MATRIX.md` | Generated human review view of the same descriptor policy. |

The JSON contains public, authenticated, and internal procedures. Public edge
route artifacts still omit internal methods; the PDP inventory retains them so
service-to-service admission can use the same contract.

## Method contract

Every entry is keyed by canonical `/package.Service/Method` procedure and
records request/response types, streaming shape, Codefly owner, source proto,
compatibility tier, the complete `saas.policy.v1.MethodPolicy`, and the edge
rate-limiter backend failure mode. `policy_sha256` fingerprints the
deterministic protobuf encoding of the complete policy. Consumers can store the
fingerprint in decisions and audit records without copying secret request data.

The compiler rejects an unknown schema, missing or unsorted identity, owner
drift, unspecified policy enums, invalid permission/scope or audit vocabulary,
incomplete resource bindings, impossible public-policy requirements, invalid
authentication-factor classification, policy hash drift, and unsupported
limiter failure behavior.

## Enforcement boundary

The gateway consumes only policy it can enforce without decoding a domain
request:

- public versus authenticated exposure;
- rate-limit class and fail-open/fail-closed backend behavior;
- the dedicated per-client-IP login-factor attempt budget;
- a stable policy fingerprint for diagnostics and audit correlation.

Authentication- and MFA-class methods fail closed when the
limiter backend is unavailable, preserving the prior security behavior without
matching URL strings. Exactly two login MFA completion methods declare
`authentication_factor_attempt`; the rate limiter receives that generated flag
instead of inferring it from REST or Connect paths.

Tenant relationships, permissions, scopes, platform roles, resource lookups,
MFA freshness, idempotency, and audit emission remain enforced by the accounts
interceptors/handlers and domain policy adapter. The generated artifact is an
input to those checks, not a replacement for state-dependent rules or Postgres
RLS.

## REST behavior

Connect and generated REST routes join the authorization lookup by canonical
procedure and fail startup if metadata is missing or internal. The generated
REST projection repaired a stale rule that incorrectly required an access token
for public `POST /v1/users` registration. Descriptor-equivalent YAML has been
removed; attempting to reintroduce one of those routes as an extension fails
startup.

Non-protobuf extension routes such as magic links and provider webhooks remain
on explicit YAML policy. They are never guessed into a protobuf method. See
`REST_SURFACE.md` for the route and OpenAPI boundary.

## Generation and CI

After Codefly protobuf generation, run from `services/accounts/code`:

```sh
go generate ./pkg/business
go generate ./pkg/adapters
go generate ./pkg/cataloggen
```

The last command emits `authz-methods.json`, the auth-gateway lookup, and
target-neutral gateway routes. Tests verify 121-method projection parity,
policy hashes, seven internal methods, two factor-attempt methods, deterministic
bytes, checked-in runtime source, REST joins, internal-route rejection, and
limiter behavior. CI regenerates both authorization artifacts and rejects any
diff.
