# Generated frontend catalog

Status: active (`P1-GEN-006`, complete).

The frontend catalog turns the normalized accounts service catalog into one
finite TypeScript boundary for clients, permissions, API-key scopes, and
entitlements. Frontend code does not maintain a second list of service
descriptors or product keys.

## Generated surface

`services/frontend/code/src/gen/saas/accounts/v1/frontend_catalog.ts` exports:

- `ACCOUNT_SERVICE_DESCRIPTORS`, `AccountsClients`, and
  `createAccountsClients(transport)` for all 24 accounts services;
- `PERMISSIONS`, permission metadata, finite exact/wildcard grant types, and
  `isPermission` for the 21 canonical RBAC values;
- `API_KEY_SCOPES` and scope types for the 19 permissions that may be delegated
  to an API key;
- `ENTITLEMENTS`, feature/quota metadata, and `isEntitlement` for the five
  server-owned product keys.

The client factory accepts the application's configured Connect transport. It
does not create a second transport or embed endpoint configuration.

## Sources of truth

Service descriptors and method policy come from the registered protobuf graph.
`pkg/business/service_vocabulary.go` owns the accounts permission and
entitlement vocabulary. The normalized `generated/service-catalog.json`
combines them so every downstream producer sees the same validated contract.

Runtime Go entitlement checks use the same exported keys. Quota interception
only maps operations with authoritative accounting: API-key creation and
organization invitations. Future quota mappings must land with a product key
and a real usage/cardinality source.

## Frontend use

Import generated constants and types instead of spelling service-owned values:

```ts
import {
  ENTITLEMENTS,
  PERMISSIONS,
  createAccountsClients,
} from "@/gen/saas/accounts/v1/frontend_catalog";

const clients = createAccountsClients(apiTransport);
const permission = PERMISSIONS.USERS_READ;
const quota = ENTITLEMENTS.SEATS;
```

The role matrix may use generated finite wildcard grants such as `users:*`, but
the permission requested by a UI gate must be an exact generated `Permission`.
This remains display logic only; accounts is always the authorization authority.

Backend entitlement responses cross a runtime boundary and therefore pass
through `isEntitlement` before becoming the generated `Entitlement` type.
Unknown keys fail visibly instead of entering frontend state as unchecked
strings.

## Generation and parity

From `services/accounts/code`, run:

```sh
go generate ./pkg/business
go generate ./pkg/cataloggen
```

The frontend generator strictly deserializes and validates the normalized
catalog before rendering. Generation fails on unknown method permissions or
scopes, malformed vocabulary, a service split across source modules, or
colliding TypeScript identifiers.

Go tests prove byte-for-byte determinism and checked-in output parity. Vitest
independently counts 24 descriptors and 121 procedures and pins the complete
permission, scope, entitlement, and client-factory surfaces. CI regenerates the
module and rejects any diff.

The separate compile-time page/plugin/navigation projection consumes these
permission types and is documented in `FRONTEND_PLUGINS.md`.
