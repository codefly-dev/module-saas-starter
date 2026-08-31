# Getting Started with saas-starter

This module provides a complete SaaS foundation.

## Services

- **store**: PostgreSQL database for users, orgs, teams, roles, sessions, entitlements (postgres agent)
- **vault**: Vault for JWT signing keys and API key hashing (vault agent)
- **accounts**: generated gRPC + Connect + opt-in REST APIs for tenant,
  permission, entitlement, billing, and usage operations (go-grpc agent)
- **auth-gateway**: Envoy ext_authz sidecar for JWT/API-key validation and canonical identity stamping (go-grpc agent)
- **cache**: Redis cache (redis agent)
- **frontend**: Next.js app with plugin-extensible admin dashboard (nextjs agent)

## Interface

This module exposes only:

- `frontend/http` — the public product application
- `auth-gateway/grpc` — module-visible Envoy ext-authz

Accounts transports and the auth-gateway HTTP gateway stay private behind the
frontend's same-origin API proxy.
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
# From the repository root. The module declares frontend as its service entry,
# so Codefly starts the complete product dependency graph.
codefly run service --fixture dev-admin
```

Open the public HTTP URL printed for `frontend`. Next.js forwards exact API
routes to the private auth-gateway gateway; the browser never receives an
internal service address. Stop the complete stack with Ctrl-C. Use
`codefly run service frontend --fixture dev-admin` with older Codefly releases
that do not yet resolve module service entries.

The local Codefly `security` workspace configuration supplies the MFA step-up
window, completion rate limit, and WebAuthn relying-party ID. Frontend stamps
its actual browser origin onto API proxy requests using Codefly's internal
service credential; auth-gateway verifies that credential before forwarding
the origin to Accounts. No local port is copied into configuration. For a
hosted environment set `WEBAUTHN_RP_ID` to the application host without
scheme/port.

## Adding this module to a workspace

```bash
codefly add module --agent saas-starter <your-module-name>
```
