# Getting Started with saas-starter

This module provides a complete SaaS foundation.

## Services

- **store**: PostgreSQL database for users, orgs, teams, roles, sessions, entitlements (postgres agent)
- **vault**: Vault for JWT signing keys and API key hashing (vault agent)
- **accounts**: generated gRPC + Connect + opt-in REST APIs for tenant,
  permission, entitlement, billing, and usage operations (go-grpc agent)
- **auth-sidecar**: Envoy ext_authz sidecar for JWT/API-key validation and canonical identity stamping (go-grpc agent)
- **cache**: Redis cache (redis agent)
- **frontend**: Next.js app with plugin-extensible admin dashboard (nextjs agent)

## Interface

This module exposes only:

- `auth-sidecar/rest` — the public application ingress
- `auth-sidecar/grpc` — module-visible Envoy ext-authz

Accounts transports and the frontend stay private behind the auth sidecar.
Internal product RPC export is tracked separately by `P1-NET-007`.

## Usage metering

The Starter includes a generic idempotent event ledger and atomic monthly hard
caps through `saas.accounts.v1.UsageService`. Installed products define meter
keys and limits through additive migrations, then use the generated protobuf
client. See `USAGE_METERING.md` before integrating a producer.

## User settings

Every Starter ships with typed appearance, regional, email, and notification
preferences in both Go and the frontend. See `SETTINGS.md` for default,
presence, reset, and extension rules.

## Development

```bash
# From the repository root. The module declares auth-sidecar as its service
# entry, so Codefly starts the complete eight-service dependency graph.
codefly run service --fixture dev-admin
```

Open the public HTTP URL printed for `auth-sidecar`. That gateway routes the
private frontend and API; do not use the frontend's direct development URL for
the application. Stop the complete stack with Ctrl-C. Use
`codefly run service auth-sidecar --fixture dev-admin` with older Codefly
releases that do not yet resolve module service entries.

The local Codefly `security` workspace configuration supplies the MFA step-up
window, completion rate limit, and WebAuthn relying-party ID. Auth-sidecar
obtains its public origin from the Codefly SDK and passes it through the trusted
gateway boundary, so no local port is copied into configuration. For a hosted
environment set `WEBAUTHN_RP_ID` to the application host without scheme/port.
Set `WEBAUTHN_RP_ORIGINS` only when supporting intentional traffic that
bypasses auth-sidecar.

## Adding this module to a workspace

```bash
codefly add module --agent saas-starter <your-module-name>
```
