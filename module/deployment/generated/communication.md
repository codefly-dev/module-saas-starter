# Event communication

_Generated from `event-catalog.json` by module-compose. DO NOT EDIT._

Every domain event type, who publishes it, and who consumes it. The machine-readable projection is [`asyncapi.json`](./asyncapi.json).

## installation.created

- **Publisher:** installation
- **Visibility:** internal
- **Schema:** `saas/events/v1/events.proto#EventEnvelope` (major v1)
- **Partition:** `{tenant_id}`
- **Retention:** 30d
- **Consumers:** _none_

## installation.revoked

- **Publisher:** installation
- **Visibility:** internal
- **Schema:** `saas/events/v1/events.proto#EventEnvelope` (major v1)
- **Partition:** `{tenant_id}`
- **Retention:** 30d
- **Consumers:** _none_

## reference.console.viewed

- **Publisher:** reference
- **Visibility:** tenant
- **Schema:** `saas/events/v1/events.proto#EventEnvelope` (major v1)
- **Partition:** `{tenant_id}`
- **Retention:** 30d
- **Consumers:**
  - reference (queue `reference.ingest`, delivery unordered)

## scope.granted

- **Publisher:** scope
- **Visibility:** tenant
- **Schema:** `saas/events/v1/events.proto#EventEnvelope` (major v1)
- **Partition:** `{tenant_id}`
- **Retention:** 30d
- **Consumers:** _none_

## scope.revoked

- **Publisher:** scope
- **Visibility:** tenant
- **Schema:** `saas/events/v1/events.proto#EventEnvelope` (major v1)
- **Partition:** `{tenant_id}`
- **Retention:** 30d
- **Consumers:** _none_
