# Generic usage metering

Status: event-meter and atomic cardinality-quota foundations implemented;
provider reporting and reconciliation remain open under `P2-ENT-004`.

The Starter owns the generic tenant, entitlement, quota, and metering
boundary. Installed products define their meter names and emit operations;
they do not create product-specific usage tables or fork the Starter API.

## What is metered

Use the event ledger for monotonically increasing activity such as API calls,
jobs, generated artifacts, processed bytes, or compute units. Use a canonical
lowercase key such as `jobs_monthly` or `compute_units_monthly`.

Seats, API keys, and similar live resource counts are cardinality gauges. Their
authoritative rows are counted directly, so they must not also be emitted as
usage events. Creating and deleting a resource changes the gauge without an
inverse usage event. Seat admission (direct members and pending invitations)
and API-key creation serialize the authoritative count and insert in one tenant
transaction. Expired pending invitations and expired or revoked API keys do not
consume capacity.

## Contract

`saas.accounts.v1.UsageService` is generated from
`services/accounts/proto/saas/accounts/v1/usage.proto`.

- `ConsumeUsage` is an internal service-to-service command.
- `GetUsage` is an authenticated tenant read requiring organization
  membership, `entitlements:read`, and the equivalent API-key scope when an
  API key is used.
- Periods are UTC calendar months; `period_end` is exclusive.
- Quantities are positive integers. Negative corrections are deliberately not
  accepted by the ingestion command.
- Limits use `-1` for unlimited, `0` for disabled or unknown, and a positive
  value for a hard cap.
- Both accepted and rejected attempts receive immutable receipts.

`idempotency_key` identifies one logical product operation within an
organization. A retry with the same key and payload returns the original
receipt and does not increment the aggregate. Reusing the key with different
meter, quantity, event time, or dimensions fails with `AlreadyExists`.

## Product integration

An installed product integrates without changing the Starter contract:

1. Add each meter and its per-plan limits through the product's additive
   Postgres migration source. Unknown meters remain disabled instead of
   silently becoming unlimited.
2. Generate the protobuf client with Codefly; do not hand-maintain HTTP DTOs
   or a second transport.
3. Derive a stable idempotency key from the product operation, for example
   `operation_kind:<operation_uuid>`. Never generate a fresh retry key.
4. Send immutable, bounded dimensions useful for reconciliation. Do not place
   secrets, user content, or unbounded identifiers in dimensions.
5. Treat `accepted=false` as a quota rejection. A successful RPC does not by
   itself mean the operation was admitted.
6. For completion-based billing, publish the usage command from a durable
   product outbox after the product transaction commits. Retry until the
   receipt is stored. This closes the product-commit/RPC-failure gap without
   relying on distributed transactions.

For operations that must be blocked before work begins, consume before the
irreversible work and proceed only when the receipt is accepted. Reservation,
release, negative correction, and prepaid-credit semantics are intentionally
not hidden in this counter API; products needing them require an additive
ledger contract.

## Storage and concurrency

Migration `60_usage_metering` replaces the old mutable monthly counter with:

- `usage_events`: immutable accepted/rejected receipts and global per-tenant
  idempotency keys;
- `usage_totals`: transactionally maintained monthly aggregates used by quota
  checks and dashboards.

Consumption resolves the effective plan or tenant override and updates the
aggregate in one tenant transaction. An advisory transaction lock serializes
the idempotency key, and a row lock serializes each tenant/meter/month total.
Concurrent requests therefore cannot both pass the same remaining hard cap.

Cardinality admission uses a separate transaction-scoped advisory lock for each
tenant and feature. The effective entitlement, authoritative live-resource
count, and resource write all run while that lock is held. Storage rejects lock
attempts outside an explicit tenant transaction so future call sites cannot
accidentally split the check from the write.

Both tables use forced tenant RLS with no application-settable bypass branch.
The request role can append events and maintain aggregates but cannot update or
delete history. Future reconciliation and billing workers must receive a
dedicated role and narrowly reviewed policies; they must not weaken the tenant
request policy.

## Deployment boundary

The current accounts runtime admits internal gRPC on its private REST h2c
listener. That listener is not exported as a module interface. Product modules
must depend on the generated named internal endpoint once `P1-NET-007` adds
multiple same-API endpoint support to the Codefly Go runtime. Do not export the
mixed REST listener or route `ConsumeUsage` through the public auth sidecar as
a workaround.

This deployment limitation does not change the protobuf or database contract;
it only blocks the final cross-module service edge.

## Remaining production work

- Generate the named internal Codefly endpoint and product dependency edge
  (`P1-NET-007`).
- Add reconciliation jobs, operational metrics, and discrepancy alerts.
- Report billable aggregates to the configured billing provider with durable
  checkpoints and idempotency.
- Add correction/reversal semantics only if a concrete product requires them.
