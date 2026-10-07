# ADR 0009: Audit store swap — a warehouse as the audit store of record, Postgres as its transactional queue

- Status: Proposed
- Date: 2026-10-02
- Supersedes, in part: [ADR 0006](./0006-audit-sink-and-retention-tiers.md) — its
  sink decision, "a tee, not a swap" (Decision item 2; option C of its Options
  considered), and for a deployment that selects a swap value, its retention
  tiers (Decision item 3).
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
   records on its resolution transaction (`pkg/auth/pg/resolver.go`), which carry
   no tee job. Postgres lacks everything older than its window. Every reader has
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

   A deployment that selects a swap value sets, beside `AUDIT_SINK`:
   - the warehouse: `AUDIT_BIGQUERY_PROJECT` and `AUDIT_BIGQUERY_DATASET` for
     `bigquery`; `AUDIT_CLICKHOUSE_DSN` and `AUDIT_EVENTS_RETENTION_DAYS` for
     `clickhouse`, which may additionally set `AUDIT_CLICKHOUSE_CLUSTER` for
     replicated tables;
   - `AUDIT_ARCHIVE_URL` — the locked archive as a URL whose scheme picks the
     archive writer (`gs://<bucket>` for GCS or `s3://<bucket>` for S3; any
     scheme without a writer is refused at startup);
   - `AUDIT_CONTENT_RETENTION_DAYS` — the details window of item 5;
   - `AUDIT_DEPLOYMENT_ID`, stamped on every record;
   - optionally, the relay's tuning: `AUDIT_RELAY_BATCH_SIZE` (the most events
     one delivery carries; default 500, at most 5000) and `AUDIT_RELAY_MAX_WAIT`
     (how long a partial batch waits for more events before it is delivered
     anyway; default 5s, at most 60s).

   The role-catalog importer, a deploy step that records audit events apart from
   the service, is the one process with no default: it refuses to run when
   `AUDIT_SINK` is unset or blank. A deployment on the `postgres` default
   therefore sets `AUDIT_SINK=postgres` explicitly wherever the importer runs.

   A missing required setting fails startup. `AUDIT_EVENTS_RETENTION_DAYS` is a
   ClickHouse setting only — BigQuery's events table has no expiry the kit sets
   (item 5) — and ClickHouse requires it to be at least the content-detail
   window, since details must not outlive their event.

   Credentials never go in configuration. BigQuery and GCS use the platform's
   ambient identity (on Kubernetes, the pod's workload identity). An `s3://`
   archive uses the AWS SDK's default credential chain and also needs
   `AWS_REGION` — startup refuses without it — and `AWS_ENDPOINT_URL_S3` when
   the bucket is in an S3-compatible store rather than S3 itself. The one
   credential that has no ambient identity is `AUDIT_CLICKHOUSE_DSN`, which
   carries the ClickHouse user and password: it is a secret, delivered to the
   service the way the deployment delivers its other secrets, never plain
   configuration, and never logged or echoed in an error.

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

   A relay drains the queue, one relay at a time under a lease:
   - in **batches**, taken oldest queue row first;
   - **at-least-once, keyed by event id** — a batch redelivered after a crash
     may leave an event in the warehouse twice, as it may in the archive, and
     every read returns each event once by event id (item 4);
   - **in no guaranteed order.** The relay reads a row only when its writing
     transaction took its id before the oldest transaction still running, so it
     never reads a row that has not committed. That gate is transaction
     visibility, not sequence number: a row can be delivered ahead of a lower
     sequence number whose transaction has yet to commit. Nothing depends on
     delivery order: both stores key a record by event id, and every read sorts
     by event time;
   - each batch is **written as one object to the locked archive first, then
     appended to the warehouse** (item 5) — the archive copy is written within
     the batch, not filled in later, and a failure between the two writes
     repeats the archive write, not the warehouse's. The relay keeps, for each
     queued row, which of the two writes has acknowledged it and what it decided
     about it, so an outage of one side does not write that row to the other side
     again; only a restart forgets, which is the redelivery above;
   - the batch's queue rows are **deleted only after both writes are
     acknowledged**;
   - a row is **set aside only on the store's own word.** The relay quarantines
     a row only when the store reports that it permanently refused that row
     (`business.PermanentRowRejection`), or when the row's details cannot be
     serialized. BigQuery reports a row it lists as `invalid` in a streaming
     insert, a row too large for any request, and a value that cannot be
     encoded; ClickHouse reports a value its driver cannot encode, and a row
     refused with an error code that says the row's own content is malformed,
     found by splitting the refused block down to single rows. Every other
     failure — a transport or server error, a throttle, a quota, a timeout, a
     failed quorum, an error the store does not recognize — is retried with
     backoff and sets nothing aside, and the relay infers nothing from how often a
     row failed. After a write that names refused rows, the relay sets those aside
     and writes the rest again. One refusal is read as a whole and not taken at
     its word: a write of two or more rows refused in every row for one and the
     same reason (`PermanentRowRejection.Reason`) is a table the store cannot
     take, not a set of bad rows, so nothing is set aside, the rows stay queued
     and the delivery is retried with backoff, and the queue's depth and age raise
     the relay-lag alert. The adapters read each insert of their own the same way,
     and write a batch's details before its events, giving an event an events row
     only once its details are accepted, so that a refused event is not left in
     the events table with an empty payload;
   - a set-aside row does not stall the queue. It moves, whole, in the same
     transaction that deletes the delivered rows, to an `audit_event_quarantine`
     table beside the queue, and is counted in `saas.audit_queue.quarantined`.
     The reason kept with it starts with a class: `warehouse_rejected_archived:`
     (the store refused the row; the archive already holds a copy, and so does the
     quarantine) or `unserializable_not_archived:` (the details cannot be
     serialized, so the archive's line for the event cannot be written and the
     quarantine holds the only copy). The kit never deletes a quarantined row;
     replaying one is not built.

   The queue is monitored: `saas.audit_queue.depth`, `saas.audit_queue.oldest_age`
   and `saas.audit_queue.quarantined` report how much is waiting, for how long,
   and how many rows are set aside. When the queue cannot be read — the latest
   read failed, or none succeeded within twice the read interval — the three go
   absent rather than staying at their last value, and each failed read counts in
   `saas.audit_queue.snapshot_errors`. The monitoring runs under every sink, not
   only a swap value: under a non-warehouse sink a non-empty queue — rows left
   by an earlier swap, which nothing drains — is logged at startup.

   The idempotency reservation (`audit_event_idempotency`) stays in Postgres
   beside the queue row, as it is today.

3. **Adapters live inside the kit.** They are Go, in the accounts service, behind
   one `AuditStore` interface carrying both halves:
   - **write** — append a batch;
   - **read** — list (keyset-paginated, as `QueryAuditLog` is), aggregate (as
     `AggregateAuditLog`), export, and the readable-source query
     (`LatestSourceSyncEvents`, which feeds `LatestSourceSyncRequests`).

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
   API. Each read session carries a server-side row restriction: the deployment,
   the caller's organization (unless the read is the platform read), the time
   window, so partitions outside it are pruned, and the envelope filters the read
   names — event types, actor, resource, client. The service then sorts, pages,
   deduplicates, joins content details and aggregates the rows it streams back,
   with the semantics of the Postgres reads.

   What is bounded is a read window, not a read. A read walks time in windows,
   newest first, and counts what one window holds in memory — the content details
   it joins and, for a read that must see every event of the window before it can
   answer (an aggregation, an export), the events and the set that deduplicates
   them — against a byte budget, `ReadConfig.WindowBytes` (64 MiB by default; the
   service exposes no setting for it). What is counted is what is kept, once per
   event however many copies a redelivery left in the tables: the copies of an
   event share its occurrence, so no narrower window could shed them. An event
   is kept as its text: the decoded payload a filter or an aggregation reads is
   several times that text, so it is dropped once the event is kept and decoded
   again when an aggregation reads it, and the count is what the window holds.
   A window that passes the budget is read again as two halves. What outlives a
   window is the answer: a page, an aggregation's buckets, an export's events.
   Four limits remain:
   - A read whose events at a single instant alone pass the budget cannot be
     split further and fails (`ErrReadTooDense`).
   - An export is the one answer as large as the history it matches. One export
     gathers at most 32 MiB of event text from either warehouse
     (`AuditExportMaxBytes`) and is refused with `ResourceExhausted` past it. The
     export request carries no time range, so an actor or an event type is all
     that narrows it.
   - An aggregation keeps more than its buckets while it reads: a float per
     percentile input and a string per distinct value, over every window, so a
     read with no time range can outgrow a pod while it returns one bucket. The
     aggregator counts that state — 8 bytes a sample, a value's text and entry,
     a bucket's keys and metrics — against a bound of 32 MiB
     (`AuditAggregateMaxBytes`, `ReadConfig.AggregateMaxBytes` and
     `clickhousestore.Config.AggregateMaxBytes`), and the read is refused with
     `ResourceExhausted` (`ErrAuditAggregateTooLarge`) as soon as it passes it,
     never answered in part. The ClickHouse statement counts the same state
     itself and returns neither keys nor percentile inputs once it would pass the
     bound (the service holds each input twice while it reads one, so there it is
     16 bytes), so the service never receives what it could not keep. A time
     range, an actor or an event type narrows it, as does grouping by fewer
     dimensions.
   - The history copy (item 7) applies its own 32 MiB source budget and bounded
     warehouse details joins, splitting busy windows before writes. It streams
     every stored event copy through verification instead of retaining copies.

   The reason is the grants. The service that reads is the service that
   appends, so it holds `bigquery.tables.updateData`. With
   `bigquery.jobs.create` beside it, that identity could run DELETE, UPDATE and
   MERGE against the store of record. So the identity is granted
   `bigquery.datasets.get`, `bigquery.tables.create` (the adapter creates its two
   tables when they are missing), `bigquery.tables.get`,
   `bigquery.tables.updateData` (the append), `bigquery.tables.getData`,
   `bigquery.tables.list`, and `bigquery.readsessions.create`, `.getData` and
   `.update`, and no job permission at all.

5. **Retention tiers.** Every registered event type has a **retention class**
   in the typed registry (ADR 0003):
   - **security** — the record of who could get in and what they were allowed to
     do: authentication (including failures), credentials issued and revoked,
     permission, role, membership, sharing and delegation changes,
     administrative and operator actions, data leaving the platform (exports,
     replays), and configuration changes. The registry's declarations
     (`pkg/business/audit_registry.go`) are the source of truth for which types
     are in this class;
   - **content** — everything else: what happened to a tenant's own content and
     operations.

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
     details as well for security-class types. An event's outcome stays where it
     is today: in the event type's name (`saas.approval.denied`) or in the
     payload's `outcome` field. How long the table keeps a row depends on the
     warehouse. On ClickHouse, `AUDIT_EVENTS_RETENTION_DAYS` sets the table's
     TTL. On BigQuery the kit creates the events table with no partition
     expiration, and refuses at startup an events table whose partitions expire,
     since its identity holds no `bigquery.tables.update` to change one. Both
     tables must use daily partitions and have no whole-table expiration,
     including expiration inherited from the dataset when they are created.
     Startup also rejects `REQUIRED` columns where the writer can supply NULL,
     and extra `REQUIRED` columns without defaults. Nullable extensions and
     omitted extra columns with defaults remain compatible;
   - a **details** table holding the full details of content-class types, kept
     for the shorter content window, `AUDIT_CONTENT_RETENTION_DAYS` — a
     partition expiration on BigQuery, a table TTL on ClickHouse.

   The **compliance record is the locked object-storage archive**, one object
   per relay batch: the full event for security-class types, and for
   content-class types the envelope plus the SHA-256 of the details, never the
   details themselves. Locked means write-once under a retention lock the
   operator cannot shorten, set to the deployment's compliance window (ADR
   0006's sizing assumes seven years). The warehouse's events table serves
   reads; it is not what a compliance regime relies on. The first two writers
   target GCS Bucket Lock and S3 Object Lock. Azure Blob immutability policies
   still need a writer; an on-premises S3-compatible lock must be qualified
   before use.

   Archive objects are named per batch and created only if absent. A batch
   delivered again after a restart is composed anew and written as a new object,
   so the archive may hold an event more than once. Every archive reader —
   including the history-copy verification (item 7) and any compliance export —
   deduplicates by event id, and the per-event hash makes duplicates provably
   identical.

   The content window is deployment configuration, not code; so is the lock
   period of the archive bucket, and the events TTL on ClickHouse.

6. **ClickHouse operating requirements.**
   - Inserts are batched: one insert per table per relay batch. A single-row
     insert creates a data part per insert and ends in too-many-parts errors.
     Every insert explicitly sets `async_insert=0` and
     `wait_for_async_insert=1`, overriding DSN and user-profile defaults: the
     relay deletes a queue row on the insert's acknowledgement, and
     an insert acknowledged before its data is stored would let the relay delete
     the only copy.
   - A single-node deployment uses MergeTree and sends neither the quorum nor
     the sequential-read setting below. When `AUDIT_CLICKHOUSE_CLUSTER` is set,
     tables are created `ON CLUSTER` as `ReplicatedMergeTree` with ClickHouse
     Keeper; the cluster must exist before the adapter starts, and a table that
     already exists must be `ReplicatedMergeTree` as well: a `MergeTree` table
     lives on one node, where no quorum can make an insert durable, so startup
     refuses it by naming the table and its engine rather than accepting it for
     its columns and TTL. Every insert then
     carries `insert_quorum=auto` (a majority of the replicas must hold it),
     `insert_quorum_parallel=0` (one quorum insert at a time) and a 60-second
     `insert_quorum_timeout`, and every read carries
     `select_sequential_consistency=1`, so a read is served only by a replica that
     holds every acknowledged insert; a replica that has not caught up fails the
     read, which the caller retries. A majority of two replicas is both of them,
     so on a two-replica cluster one replica down stops appends: the insert fails
     at the quorum timeout, the relay retries with backoff, and the queue grows
     until the replica returns.
   - Existing tables must use `MergeTree` or `ReplicatedMergeTree`; a declared
     cluster requires the latter. Engines that sum, collapse, aggregate or
     replace rows are refused on both deployment shapes:
     merges must preserve every audit envelope and its details hash, including
     duplicate rows left by redelivery.
   - The adapter's ClickHouse user holds `CREATE TABLE`, `INSERT` and `SELECT` on
     the database and no `ALTER`, `DELETE`, `UPDATE`, `TRUNCATE` or `DROP`, so it
     cannot rewrite the store of record.
   - Tables are partitioned by month, and a table TTL expires each table at its
     window: `AUDIT_EVENTS_RETENTION_DAYS` for events,
     `AUDIT_CONTENT_RETENTION_DAYS` for details.
   - Growing past one shard is a planned operation, not an automatic one — or
     the deployment uses a managed ClickHouse with shared storage.

7. **History migration.** Switching a deployment from `postgres` to a swap value
   includes a one-time copy of the existing `audit_events` rows into the
   warehouse and the archive, verified per organization by row counts and by
   per-row hashes. Once verified, the copied monthly partitions are removed
   with `DROP TABLE`, as `RunRetention` drops expired ones, so no row is
   deleted and the append-only triggers stay as they are. The removal is one
   database function (migration 32) that takes an explicit list of the
   verified partitions, locks them, re-counts each against the count that was
   verified, and refuses on any mismatch — a partition that gained a row since
   verification is never dropped. `-through`, the cutoff of the copy, accepts
   completed months (UTC) only. The copy starts with UTC-day windows, halves
   them when source records or warehouse details would exceed their byte
   budgets, and refuses an instant that cannot fit. Source records retain
   canonical text alone after counting the current decoded payload; read-back
   retains bounded unique details and verifies stored event copies as they
   stream in. Response blocks and one current row also consume memory. Windows
   are visited oldest first, so splitting never changes the source digest.
   A post-append overflow ends the run safely and is resumed by the next run.
   From then on `audit_events` receives no new rows, and Postgres holds
   only the queue.
   Missing content details get one repair attempt per event per run, even after
   several partial writes: duplicate events rows are not evidence of permanent
   refusal. Verification still checks every copy and blocks removal while any
   required details are missing or differ.

8. **Conformance.** Every adapter passes one shared test suite: read
   deduplication (after a batch is appended twice, every read returns each
   event once by event id), batch append, list, aggregate, export and
   readable-source parity with the Postgres implementation over the same
   fixture, refusal of a query without tenant scope, and retention-class routing
   (a content-class event's full details land only in the details table, and
   only its content-free form in the archive).

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
| Archive object names fixed by a batch's content, so a redelivery after a restart cannot duplicate | Needs the same batch composition on every redelivery — more moving parts for no compliance gain. |
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

- `audit_sink.go` and `pkg/auditstore/auditsink` — `auditsink.Load` admits
  `bigquery` and `clickhouse`, reads and validates their settings, and
  `auditsink.Open` builds the adapter and the archive writer; `audit_sink.go`
  starts the relay on them. The `external` refusal stays; its message, which
  today says Postgres is the source of truth under every value, is reworded.
  The one-time history copy resolves the sink through the same
  `auditsink.Load` as the service, so a deployment configures the swap once and
  the copy never writes to a different store than the service reads. The
  role-catalog importer reads `AUDIT_SINK` alone, through
  `auditsink.RequireMode`, and refuses an unset or blank value rather than
  defaulting to `postgres`: under a swap value a default would write its events
  to `audit_events`, where nothing reads them.
- `pkg/business/audit.go` — under a swap value, the emitter's write inserts into
  the queue table in place of `audit_events`, for org-scoped and platform-level
  entries alike. `EmitTx`'s transaction contract, `VerifyAuditWiring` and the
  domain-event publish are unchanged. `AggregateAuditLogForReader` runs the
  reader's grant check and the aggregate in one tenant transaction today; under
  a swap value it becomes grant check in Postgres, then the aggregate in the
  warehouse — two steps, no shared snapshot.
- `pkg/auth/pg/resolver.go` — `emitSsoJitAudit` and `emitUserRegistered` record
  their events on the resolution transaction through the emitter's `RecordTx`
  rather than inserting into `audit_events` themselves. `RecordTx` writes the
  record alone — the `audit_events` row, or under a swap value the queue row —
  with no domain event and no tee job, so the swap does not lose them.
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
- `pkg/business/retention.go` — partition drop keeps running for `audit_events`;
  the history migration (item 7) removes copied partitions with `DROP TABLE`
  as well, through its own verified-partition function. Under a swap value,
  expiry belongs to the warehouse where it sets one — the details table on
  both, the events table on ClickHouse only — and to the archive's lock.
- `store/migrations` — migration 30 adds the queue table, under the same tenant
  row-level security as `audit_events` (`audit_events_tenant`, in
  `1_baseline.up.sql`), with insert granted to the writers and select and delete
  to the relay; its down migration refuses to drop a non-empty queue, since the
  rows in it are audit records no warehouse holds yet. Migration 21 adds the
  retention class to `audit_event_types`. Migration 22 adds the
  verified-partition removal function of item 7. Migration 23 adds the
  `audit_event_quarantine` table of item 2, which only the relay's role can read
  or insert into; its down migration refuses to drop a non-empty quarantine.
  `audit_events` itself is untouched: its `audit_events_no_delete` and
  `audit_events_no_update` triggers and its `SELECT`/`INSERT`-only grants stay as
  they are. Stored history in the warehouse is guarded by the service-side scope
  of item 4, not by these policies.

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
  archive bucket — and a relay whose lag and quarantine table need watching.
- On BigQuery the kit never expires the events table, and refuses to start on
  one whose partitions expire, so the table grows with the history; the locked
  archive, not that table, is the compliance record.
- Each adapter must stay at parity with the Postgres implementation; the
  conformance suite is what holds it there.
