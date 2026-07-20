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

- `auth-sidecar/http` — the public application ingress
- `auth-sidecar/grpc` — module-visible Envoy ext-authz

Accounts transports and the frontend stay private behind the auth sidecar.
Internal product RPC export is tracked separately by `P1-NET-007`.

## Usage metering

The Starter includes a generic idempotent event ledger and atomic monthly hard
caps through `saas.accounts.v1.UsageService`. Installed products define meter
keys and limits through additive migrations, then use the generated protobuf
client. See `USAGE_METERING.md` before integrating a producer.

## Development

```bash
# Enter the Nix dev shell
nix develop

# Run services
codefly run
```

The local Codefly `security` workspace configuration supplies the MFA step-up
window, completion rate limit, and WebAuthn relying-party policy. For a hosted
environment set `WEBAUTHN_RP_ID` to the application host without scheme/port
and `WEBAUTHN_RP_ORIGINS` to a comma-separated list of exact HTTPS browser
origins. Startup fails closed when this policy is missing or malformed.

## Adding this module to a workspace

```bash
codefly add module --agent saas-starter <your-module-name>
```
