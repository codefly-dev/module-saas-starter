# Getting Started with saas-starter

This module provides a complete SaaS foundation.

## Services

- **store**: PostgreSQL database for users, orgs, teams, roles, sessions, entitlements (postgres agent)
- **vault**: Vault for JWT signing keys and API key hashing (vault agent)
- **api**: gRPC + REST API for user/org/team/permission/auth operations (go-grpc agent)
- **auth-sidecar**: Envoy ext_authz sidecar for JWT/API key validation + OPA RBAC (go-grpc agent)
- **cache**: Redis cache (redis agent)
- **frontend**: Next.js app with plugin-extensible admin dashboard (nextjs agent)

## Interface

This module exposes:
- `api/grpc` — gRPC API (module visibility)
- `auth-sidecar/grpc` — Auth sidecar (module visibility)
- `frontend/http` — Web app (public visibility)

## Development

```bash
# Enter the Nix dev shell
nix develop

# Run services
codefly run
```

## Adding this module to a workspace

```bash
codefly add module --agent saas-starter <your-module-name>
```
