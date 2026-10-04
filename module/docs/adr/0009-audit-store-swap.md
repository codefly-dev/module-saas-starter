# ADR 0009: Audit store swap — a warehouse as the audit store of record, Postgres as its transactional queue

- Status: Proposed
- Date: 2026-10-02
- Supersedes, in part: [ADR 0006](./0006-audit-sink-and-retention-tiers.md) — its
  sink decision, "a tee, not a swap" (Decision item 2, option B), and for a
  deployment that selects a swap value, its retention tiers (Decision item 3).
  ADR 0006's admission policy and sizing stand.
- Refines: [ADR 0003](./0003-typed-audit-event-registry.md) — "analytics stays in
  Postgres" now holds for the `postgres` default only. The registry and the
  single write path are unchanged.
- It does not change code.

## Context

ADR 0006 kept Postgres as the only audit store of record and added an opt-in
tee: with `AUDIT_SINK=both`, the emitter enqueues an export job beside each
org-scoped audit row, and a worker posts it to an operator-configured HTTP
endpoint (`HTTPAuditSink`), one event per request. That keeps the write-path
guarantee, and it remains the right answer for a deployment that only needs a
copy somewhere else. It is not enough for a deployment whose audit must be kept
for years, locked, and queried heavily:

1. **Long retention.** Postgres holds every audit row for the
   `data_retention_policies` window (365 days by default; `RunRetention` drops
   whole monthly partitions). A compliance window of several years keeps that
   history in the operational database — ADR 0006's sizing puts seven years at
   ~2 TB for a small deployment and ~22 TB for a medium one. The tee does not
   shrink Postgres; it adds a copy.
2. **A locked archive.** A compliance regime may require a write-once copy that
   no operator can alter or delete before its window ends. The kit writes no
   such copy; the tee's destination is whatever sits behind an HTTP endpoint.
3. **Query load.** The activity list, dashboard aggregation
   (`AggregateAuditLog`: grouped counts over time buckets and payload
   predicates), exports and readable-source queries all run against the
   operational database. Dashboards make audit an analytical workload on the
   instance that serves every mutation.
4. **One store to query.** Under the tee there are two copies and neither is
   complete. The warehouse copy lacks platform-level events (no organization):
   the job platform admits a global-scope job only from the privileged worker
   pool, which cannot enqueue on the caller's transaction, so `work.go` warns
   that these stay Postgres-only. It also lacks the rows the auth resolver
   writes with raw SQL (`pkg/auth/pg/resolver.go`), which never pass through the
   emitter. Postgres lacks everything older than its window. Every reader has
   to know which copy answers which question.

The requirement ADR 0006 refused to trade away still stands: no state change
commits without its compliance record, and no external system can join a
Postgres transaction.

## Decision

1. **Swap, not tee.** `AUDIT_SINK` gains two swap values, `bigquery` and
   `clickhouse`. Under a swap value the selected warehouse is the audit store of
   record and Postgres no longer keeps audit history. `postgres` stays the
   default for every deployment that does not opt in. `both` — ADR 0006's tee —
   keeps its current behaviour, for compatibility. `external` stays refused.

   A deployment that selects a swap value sets, beside `AUDIT_SINK`: the
   warehouse (`AUDIT_BIGQUERY_PROJECT` and `AUDIT_BIGQUERY_DATASET` for
   `bigquery`; `AUDIT_CLICKHOUSE_DSN` and `AUDIT_EVENTS_RETENTION_DAYS` for
   `clickhouse`), `AUDIT_ARCHIVE_URL` — the locked archive as a URL whose scheme
   picks the archive writer (`gs://<bucket>` for GCS or `s3://<bucket>` for S3;
   any scheme without a writer is refused at startup) —
   `AUDIT_CONTENT_RETENTION_DAYS` (the details window of item 5) and
   `AUDIT_DEPLOYMENT_ID`, stamped on every record. ClickHouse may additionally
   set `AUDIT_CLICKHOUSE_CLUSTER` for replicated tables. The events retention
   must be at least the content-detail window. A missing required setting
   fails startup. Credentials are the platform's ambient identity (on
   Kubernetes, the pod's workload identity), never a key in configuration.

2. **Postgres keeps only the transactional queue.** `EmitTx` still writes inside
   the caller's transaction (and `Emit` inside its own, for the observational
   types of ADR 0003's durability amendment), but what it writes is a row in a
   **separate, short-lived queue table** — not a permanent row in
   `audit_events`. The queue row commits together with the mutation and with
   the domain event that webhook delivery fans out from, exactly as the audit
   row does today, so "no state change without its compliance record" holds
   unchanged. Every audit write goes through the queue — including
   platform-level (no-organization, control-plane) events, which the swap must
   not leave Postgres-only the way the tee does.

   `audit_events` keeps its append-only triggers and its grants unchanged; under
   a swap value it simply receives no new rows. The queue being its own table
   is what keeps the append-only guarantee intact for every `postgres` and
   `both` deployment: no trigger is relaxed to make room for a delete.

   A relay drains the queue:
   - in **batches**;
   - **at-least-once, keyed by event id** — a batch redelivered after a crash
     may leave an event in the warehouse twice, as it may in the archive, and
     every read returns each event once by event id (item 4);
   - **ordered per organization** (platform-level events form one more ordering
     key);
   - each batch is **appended to the warehouse and written as one object to the
     locked archive** (item 5) — the archive copy is written within the batch,
     not filled in later;
   - the batch's queue rows are **deleted only after both writes are
     acknowledged**.

   The idempotency reservation (`audit_event_idempotency`) stays in Postgres
   beside the queue row, as it is today.

3. **Adapters live inside the kit.** They are Go, in the accounts service, behind
   one `AuditStore` interface carrying both halves:
   - **write** — append a batch;
   - **read** — list (keyset-paginated, as `QueryAuditLog` is), aggregate (as
     `AggregateAuditLog`), export, and the readable-source queries
     (`LatestSourceSyncRequests`).

   Two adapters ship: **BigQuery** (managed, Google Cloud) and **ClickHouse**
   (self-hosted or managed, any cloud or on-premises). The existing Postgres
   reads become the third implementation of the same interface and the
   reference the other two are measured against.

4. **Reads move with writes.** Under a swap value the activity list, dashboard
   aggregation, exports and readable-source queries read from the adapter.
   Postgres row-level security does not reach a warehouse, so tenant scope is
   enforced in the service on every query: an organization's read always
   carries its organization predicate, only the platform-admin path may span
   organizations, and the adapter refuses a query that arrives without a scope.
   Adapter-native row policies (BigQuery row-level access policies, ClickHouse
   row policies) are defence in depth, not the boundary.

   Every read returns each event once by event id. The warehouse may hold an
   event more than once after a redelivery (item 2): BigQuery collapses a
   repeated streaming insert id only within a short window, and a relay that
   restarts between its writes and its delete delivers the batch again. The
   read half deduplicates exactly as every archive reader does (item 5), and
   the per-event hash makes the copies provably identical.

   BigQuery reads run no query job. They go through the BigQuery Storage Read
   API. Each read session carries a server-side row restriction: the
   deployment, the caller's organization (unless the read is the platform
   read), the time window, so partitions outside it are pruned, and the event
   types when the read names them. The service then sorts, pages,
   deduplicates, joins content details and aggregates the rows it streams back,
   with the semantics of the Postgres reads.

   The reason is the grants. The service that reads is the service that
   appends, so it holds `bigquery.tables.updateData`. With
   `bigquery.jobs.create` beside it, that identity could run DELETE, UPDATE and
   MERGE against the store of record. So the identity is granted
   `bigquery.datasets.get`, `bigquery.tables.get`, `bigquery.tables.getData`,
   `bigquery.tables.list`, and `bigquery.readsessions.create`, `.getData` and
   `.update`, and no job permission at all.

5. **Retention tiers.** Every registered event type has a **retention class**
   in the typed registry (ADR 0003):
   - **security** — authentication, failed authentication, permission and role
     changes, admin actions, data exports, configuration changes;
   - **content** — everything else.

   A code-owned type declares its class beside its durability, and cannot be
   registered without one. A type declared by a solution (its manifest) or a
   composed module (its declaration call) states its class in that
   declaration; one that states none is **content**, and a value outside the
   two is refused. A declared class only grows, as a `pii` mark does: a
   re-declaration may raise `content` to `security` and is refused if it would
   lower it, omission included.

   The retention class is its own field. `Category`'s `security` value names a
   narrower set and is not reused for it.

   The warehouse keeps two tables:
   - an **events** table holding the envelope — actor, organization, event
     type, target id, timestamp — plus a hash of the details, and the full
     details as well for security-class types; kept for the compliance window.
     An event's outcome stays where it is today: in the event type's name
     (`saas.approval.denied`) or in the payload's `outcome` field;
   - a **details** table holding the full details of content-class types, kept
     for a shorter, configurable window.

   A **locked object-storage archive** holds the compliance copy, one object
   per relay batch: full for security-class events, content-free (envelope plus
   hash) for content-class events. Locked means write-once under a retention
   lock the operator cannot shorten. The first two writers target GCS Bucket
   Lock and S3 Object Lock. Azure Blob immutability policies still need a
   writer; an on-premises S3-compatible lock must be qualified before use.

   Archive objects are named per batch. A retried batch writes a new object, so
   the archive may hold an event more than once. Every archive reader —
   including the history-copy verification (item 7) and any compliance export —
   deduplicates by event id, and the per-event hash makes duplicates provably
   identical.

   Windows are deployment configuration, not code.

6. **ClickHouse operating requirements.**
   - Inserts are batched — the relay's batches, `async_insert`, or both. A
     single-row insert creates a data part per insert and ends in
     too-many-parts errors.
   - A single-node deployment uses MergeTree. When
     `AUDIT_CLICKHOUSE_CLUSTER` is set, tables use `ReplicatedMergeTree` with
     ClickHouse Keeper; the cluster must exist before the adapter starts.
   - Tables are partitioned by month, and table TTL expires each tier at its
     window.
   - Growing past one shard is a planned operation, not an automatic one — or
     the deployment uses a managed ClickHouse with shared storage.

7. **History migration.** Switching a deployment from `postgres` to a swap value
   includes a one-time copy of the existing `audit_events` rows into the
   warehouse and the archive, verified per organization by row counts and by
   per-row hashes. Once verified, the copied monthly partitions are detached
   and dropped — the mechanism `RunRetention` already uses — so no row is
   deleted and the append-only triggers stay as they are. From then on
   `audit_events` receives no new rows, and Postgres holds only the queue.

8. **Conformance.** Every adapter passes one shared test suite: read
   deduplication (after a batch is appended twice, every read returns each
   event once by event id), batch append, list and aggregate parity with the Postgres implementation over the
   same fixture, refusal of a query without tenant scope, and retention-class
   routing (a content-class event's full details land only in the details table,
   and only its content-free form in the archive).

## Alternatives considered

The queue between the transaction and the warehouse (item 2):

| Alternative | Rejected because |
| --- | --- |
| Local disk spool in the accounts process | Crash gap between the transaction commit and the spool write: the mutation is committed and its record is nowhere durable. |
| Synchronous write to the warehouse in the request | A warehouse outage either fails the mutation or loses the event; there is no third outcome. |
| A message broker | The same dual write — commit, then publish — with the same gap, plus a new system to run. |
| Change data capture on the Postgres WAL | Still needs the Postgres row in order to capture it, and replication slots plus a connector are heavier to run than a relay over a table. |
| Queue rows in `audit_events`, with the no-delete trigger relaxed for them | Weakens the append-only guarantee on the table every `postgres` and `both` deployment keeps its history in, to serve a mode those deployments do not use. |
| Fill the archive from the warehouse later, on a schedule | Opens a window in which the compliance copy lags the store of record, and adds a second job to watch. |
| Conditional create-if-absent archive object names, so a retry cannot duplicate | Needs the same batch composition on every retry — more moving parts for no compliance gain. |
| Committed write streams with offsets, so a redelivery cannot duplicate in the warehouse | Stream state the relay must carry across restarts and recover on every one — more machinery for no compliance gain, since every read already deduplicates by event id, as archive readers do. |

Where the adapters live (item 3):

| Alternative | Rejected because |
| --- | --- |
| A separate warehouse service the kit calls | One more deployable per environment, a second queue in front of it, a second permission check, and every read becomes a network hop. Worth it only if producers or readers outside the kit needed the store directly; none do. |
| One managed warehouse per cloud (BigQuery, Redshift, Synapse) | One adapter per cloud to build and hold at parity. ClickHouse covers every non-Google environment, on-premises included, with one adapter. |

How BigQuery is read (item 4):

| Alternative | Rejected because |
| --- | --- |
| A second, query-capable identity for reads | Adds a second principal to grant, rotate and audit. And the process that appends would then hold job creation through it too: the DML risk the append-only invariant exists to prevent. |

The sink shape itself:

| Alternative | Rejected because |
| --- | --- |
| Keep the tee as the only option (ADR 0006) | Fails the four needs under Context. It stays available as `both`. |

## Consequences

**What changes in code** (paths under `module/services/accounts/code/`):

- `work.go` — `configuredAuditSink` admits `bigquery` and `clickhouse`, builds
  the adapter and the archive writer, and starts the relay. The `external`
  refusal stays; its message, which today says Postgres is the source of truth
  under every value, is reworded.
- `pkg/business/audit.go` — under a swap value, the emitter's write inserts into
  the queue table in place of `audit_events`, for org-scoped and platform-level
  entries alike. `EmitTx`'s transaction contract, `VerifyAuditWiring` and the
  domain-event publish are unchanged. `AggregateAuditLogForReader` runs the
  reader's grant check and the aggregate in one tenant transaction today; under
  a swap value it becomes grant check in Postgres, then the aggregate in the
  warehouse — two steps, no shared snapshot.
- `pkg/auth/pg/resolver.go` — `emitSsoJitAudit` and `emitUserRegistered` insert
  into `audit_events` with raw SQL on the resolution transaction, bypassing the
  emitter. Under a swap value they must go through the queue, or the swap loses
  them.
- `pkg/infra/postgres_audit.go` — `QueryAuditLog` and `AggregateAuditLog` become
  the Postgres implementation of `AuditStore`. The aggregate's category
  dimension reads `audit_event_types` in SQL; an adapter takes it from the
  registry in the service instead.
- `pkg/infra/postgres_readable_sources.go` — `LatestSourceSyncRequests` joins
  `audit_events` to `principals` in one query, inside the source-read snapshot.
  Under a swap value it becomes a warehouse read of each source's latest actor
  id — outside that snapshot — then a join to display names from Postgres,
  inside it.
- `pkg/business/audit_registry.go` — `AuditEventDefinition` gains the retention
  class, required the way `Durability` is. A declared type's class comes from
  its declaration — the manifest event, or the module declaration message —
  and is stored on its `audit_event_types` row, `content` when it states none.
- `pkg/business/retention.go` — partition drop keeps running for `audit_events`,
  and is also how the history migration (item 7) removes copied partitions.
  Under a swap value, tier expiry belongs to the warehouse (TTL or table
  expiration) and to the archive's lock.
- `store/migrations` — a new queue table, under the same tenant row-level
  security as `audit_events` (`audit_events_tenant`, in `1_baseline.up.sql`),
  with delete granted to the relay. `audit_events` itself is untouched: its
  `audit_events_no_delete` and `audit_events_no_update` triggers and its
  `SELECT`/`INSERT`-only grants stay as they are. Stored history in the
  warehouse is guarded by the service-side scope of item 4, not by these
  policies.

**What stays:**

- `postgres` is the default and behaves exactly as today: audit rows in Postgres,
  read under row-level security, retention by partition drop.
- `both` behaves exactly as today (`pkg/business/audit_jobs.go`,
  `pkg/business/audit_http_sink.go`), including its org-scoped-only coverage.
- The typed registry and its payload validation, the durability rule, the
  idempotency reservation, and webhook fan-out.
- ADR 0006's admission policy and sizing. [ADR 0008](./0008-datasource-sync-actor-provenance.md)'s
  rule that a sync's actor lives only in the audit trail — under a swap value,
  that trail is the adapter.

**Costs accepted:**

- Under a swap value, reads trail writes: an event is listed once the relay has
  delivered it, not when its mutation commits.
- A deployment that opts in runs two more systems — the warehouse and the
  archive bucket — and a relay whose lag needs watching.
- Each adapter must stay at parity with the Postgres implementation; the
  conformance suite is what holds it there.
