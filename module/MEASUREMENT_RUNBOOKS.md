# Measurement and SLO runbooks

The alert pack uses these stable runbook identifiers. Never paste event
payloads, credentials, email addresses, message bodies, or unrestricted
provider responses into an incident channel.

## `slo-burn`

1. Identify the journey, window, numerator, denominator, release, and region.
2. Confirm the denominator excludes documented policy rejections rather than
   treating all client errors as availability failures.
3. Correlate the affected request traces with bounded job, provider, and event
   IDs. Compare with the preceding release and unaffected regions.
4. Mitigate by rolling back the responsible release or isolating the failing
   integration. Do not weaken authentication, consent, quota, or schema
   validation.
5. Verify both short and long burn windows recover, then record the lost
   events or commands that require safe replay.

## `jobs`

1. Open `/admin/platform/jobs` and inspect queue depth, oldest ready age,
   attempts, lease recovery, and new dead letters. The surface is payload-free.
2. Compare `saas.jobs.polls`, `saas.jobs.active`, `saas.jobs.completed`, and
   `saas.jobs.duration` for the affected queue. Check worker health and its
   database role before changing concurrency.
3. Restore the dependency or worker first. A provider outage should leave
   product requests healthy while queue age grows.
4. Replay only dead letters after the cause is fixed. Use one stable operator
   idempotency key; replay preserves the source payload and records lineage.
5. Confirm queue age drains, no new terminal failures appear, and logical
   destination counts match distinct source identities.

## `audit-relay`

Under `AUDIT_SINK=bigquery` or `clickhouse` each audit event is a row in the
Postgres `audit_event_queue`, written in the transaction of the change it
records. The accounts relay delivers it to the locked archive and then the
warehouse, and deletes the row only after both acknowledged. Delivery is
at-least-once: a retry may duplicate a warehouse row or archive object, and
readers deduplicate by event id.

1. Read `saas.audit_queue.depth` and `saas.audit_queue.oldest_age` together.
   A zero age with zero depth means the queue has drained. **An absent series
   means the queue could not be read** (the latest read failed, or none was
   made in the last two intervals), not that it is healthy: do not infer a
   healthy relay from a missing series. `saas.audit_queue.snapshot_errors`
   counts the failed reads, and the monitor runs under every `AUDIT_SINK`, so
   the series are present whenever the service is.
2. Check the Accounts audit relay's bounded error logs and the health of its
   declared warehouse and locked archive. Confirm the deployed `AUDIT_SINK`,
   deployment id, destination, and workload identity without printing secrets.
3. Restore the failing destination or identity and let the relay retry the
   transactional queue. Do not delete queue rows, turn off audit emission,
   or send around the relay. Queue rows are never destroyed by the kit.
4. Confirm depth and age return to zero, then compare source event ids with
   distinct warehouse and archive event ids for the affected interval. Check
   that service reads still enforce organization and deployment scope.

### A row the warehouse refuses (`saas.audit_queue.quarantined`)

One row the warehouse rejects for its own content must not hold every other
organization's events behind it, and a warehouse that is merely failing must not
cost a good row its place. So the relay sets a row aside only on what the
warehouse says about that row: a permanent refusal the adapter attributes to the
event. Every other failure is retried with backoff and sets nothing aside: a
transport error, a server error, a throttle (429), a quota, a timeout, a
ClickHouse quorum or replica failure, and any error the adapter does not
recognize. A flapping or unreachable warehouse grows `saas.audit_queue.depth` and
`oldest_age`, never the quarantine. The relay draws no conclusion from what else
the warehouse accepted or from how often a row failed.

What the adapters report as a permanent refusal:

- **BigQuery**: a row that `insertAll` lists with the reason `invalid` (a value
  that does not fit its column), and a row too large for any request, which is
  found before anything is sent. A row listed as `stopped` (fine, but sent beside
  an invalid one), and every failure of the request as a whole, are retried.
- **ClickHouse**: a value the driver cannot encode as its column's type, named
  before anything is sent, and a block the server refuses with an exception code
  that means a row's content is malformed (the parsing codes 6, 26, 27, 38, 41, 72
  and 117; the type and range codes 53, 70 and 321; 131 for a string over what the
  column holds; and 469 for a table CHECK constraint). The adapter splits a block
  refused that way down to single rows to find the row; the rows around it are
  written meanwhile. Network, socket, timeout, memory, too-many-parts, quorum,
  replica, Keeper, access and missing-table codes, and any code not listed, are
  retried and never searched.

When a write names refused rows, the relay sets exactly those aside and writes the
rest again, so a batch of one is judged as a batch of five thousand. The archive
is written once, whole, before the warehouse is tried, so a row the warehouse
refused is already in the archive. A row whose own details cannot be serialized is
the exception: the archive's line for an event carries the hash of its canonical
details, which such a row does not have, so it is never archived and the
quarantine holds its only copy.

A set-aside row moves, whole and with the error, into `audit_event_quarantine`
in the same transaction that deletes the delivered rows. The `error` column starts
with the class, then the cause (bounded to 1000 bytes, valid text):

| `error` starts with | what it means | where else the row is |
|---|---|---|
| `warehouse_rejected_archived:` | the warehouse refused the row for good | the archive, whole |
| `unserializable_not_archived:` | the row's details cannot be serialized | nowhere: the quarantine holds the only copy |

The relay logs each at error level (event id, type, queue sequence number and
class, never the payload). `saas.audit_queue.quarantined` counts the rows there
and the `audit_relay_quarantine` alert fires while it is above zero. The kit never
deletes a quarantined row. Replaying or resolving quarantined rows is not built
yet: until it is, read the table as the database owner (the relay's role may read
it), fix the cause, and keep the rows. Read the causes before concluding that the
rows are at fault: reasons that agree across events of different organizations
point at the warehouse's table, which someone changed under the relay, not at the
events.

### Switching `AUDIT_SINK` back to `postgres` (or `both`)

Nothing drains the queue under `postgres` or `both`, and rows left in it are not
lost: they reach the warehouse only when a warehouse sink is restored. The
service does not refuse to start (that would take login down). At startup it logs
an error with the number of queued events, and the depth and age series keep
reporting them. To roll back for good, first restore the warehouse sink and let
the relay drain the queue to zero.

Migration 20's down refuses (`audit_event_queue still holds N queued audit
events`) while any row is queued, and migration 23's down refuses while any row
is quarantined: rolling the schema back would drop the only copy of those events.
Do not work around the refusal by deleting rows.

### The queue is full but nothing is delivered and nothing is logged

The relay hands out only rows whose writing transaction is older than every
transaction still running. Two conditions hold the whole queue behind that gate
without an error: depth and age rise, `snapshot_errors` stays at zero, and the
relay logs nothing.

- A write transaction left open: a stuck session, a long migration, a forgotten
  `BEGIN`. Look for the oldest `xact_start` in `pg_stat_activity` and end that
  transaction; the queue drains by itself behind it.
- A logical restore into a new cluster: restored rows carry transaction ids from
  the old cluster, which can be ahead of the new cluster's, so the gate never
  opens for them. Compare `min(xact_id)` and `max(xact_id)` of the queue with
  `pg_snapshot_xmin(pg_current_snapshot())`; rows whose `xact_id` is at or past
  `pg_snapshot_xmax(pg_current_snapshot())` came from another cluster. They are
  committed and complete, and re-stamping their `xact_id` below the new
  cluster's horizon (an owner-level update; the relay's role cannot) releases
  them. Nothing is lost while they wait.

## `integrations`

1. Separate transport failure, timeout, rate limiting, authentication,
   provider rejection, and invalid local configuration.
2. Use safe provider request IDs and trace IDs to correlate logs. Never log
   authorization headers or response bodies that may contain recipient data.
3. Correct endpoint, credentials, allowlist, or provider health. Respect
   provider retry guidance; do not remove bounded backoff or timeouts.
4. Re-drive eligible dead letters through job operations and verify one
   provider delivery for each logical command.

## `analytics-export`

1. Inspect only the `analytics` queue. Compare depth, oldest ready age, retry
   count, schema rejection codes, idempotency conflicts, terminal failures,
   and worker duration.
2. Validate `PRODUCT_ANALYTICS_MODE`, the regional `POSTHOG_HOST`, project ID,
   capture-key presence, and personal deletion-key presence without printing
   either key. Check the destination status page.
3. A schema reject is not retryable. Compare the event name/version/source,
   allowed properties, and privacy purpose with `registry.json`; correct the
   producer or registry deliberately.
4. Restore the sink and let bounded retries drain. Re-drive terminal provider
   failures with the original event identity after confirming the destination
   deduplicates UUIDs.
5. Reconcile distinct source event UUIDs with distinct delivered UUIDs for the
   affected interval. Record missing, rejected, and duplicate counts.

## `usage-reconciliation`

1. Select one organization, meter, and UTC period under an audited operator
   role. Sum accepted immutable `usage_events` and compare with `usage_totals`.
2. Separate exact retries, rejected quota attempts, late arrivals, and
   provider corrections. Never repair by deleting immutable receipts.
3. If the aggregate is wrong, stop provider reporting for the affected meter,
   apply a reviewed correction or rebuild procedure, and retain an audit trail.
4. Re-send the corrected provider quantity with a stable reconciliation key.
5. Verify source events, aggregate, customer history, entitlement limit, and
   provider quantity agree after the late-arrival window.
