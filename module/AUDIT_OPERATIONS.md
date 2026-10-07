# Audit warehouse operator commands

The accounts image contains its audit commands in the same executable as the
service. Use `./app audit-history-copy` and `./app audit-qualify` from `/app`.
The standalone Go commands under accounts' `code/cmd` use the same implementation.
Generated service entry points and build recipes are not modified.

Commands use the runtime's injected Codefly database capabilities and `AUDIT_*`
configuration. They run only under `AUDIT_SINK=bigquery` or `clickhouse` and never
accept database URLs in arguments. `DATABASE_URL` is an explicit integration-test
environment override. Operator output contains no credentials, payloads or raw
provider errors. A destination failure returns a safe `error_code` and a nonzero
exit status.

## Capabilities and destination preflight

`./app audit-history-copy -capabilities-json` (also supported by `audit-qualify`)
returns `codefly/audit-tools/v1`, the history and qualification report schemas,
`expected_partitions_sha256: true`, and a nonsecret `configuration` map. It does
not connect or mutate. An operator must verify that map against the intended
warehouse, archive, deployment and content-retention policy before running a
command. The map deliberately excludes a ClickHouse DSN and all database secrets.

## History copy, verification and removal

Run `./app audit-history-copy -json -through YYYY-MM` to copy through the selected
month into the warehouse and archive. `-through` names the last month copied and
must be a **completed month in UTC**: the current month and later ones are refused
as a usage error. Without it the run covers every partition, which can verify but
cannot drop. `-verify-only` writes nothing.

The run is resumable. For each day-long window of each partition it reads the
source rows, reads back what the warehouse holds for that window, appends only the
events whose stored copies are not complete, reads the window back again, and then
verifies every source row against every stored copy of its id. Existing copies are
therefore verified **after** the append, in the same pass; an earlier run's copies
are not verified before new rows are written. A verification failure leaves the
partitions in place.

An event is appended when the warehouse holds none of it, and again when it holds
it without details that verification requires and a write supplies: a content-class
event inside the content window whose details row is not there. Both adapters write
an event's events row before its details row, so a run whose details write failed
leaves exactly that behind, and the next run appends the event again, events row
and details row. The details are attached to every copy of the event by its id, so
the copy already in the warehouse is complete too. A text-mode run counts these as
`rewritten` in its progress lines, apart from the events `copied`. Content-class
events older than the content window may have no details (the warehouse expired
them) and are not appended.

A copy that is wrong in itself is **not** appended again: the warehouse is
append-only and every copy is verified, so the wrong copy would stay. These are
verification failures for the operator to look at: a copy stored under another
deployment id, with an envelope or retention class the row does not have, with a
details hash that is not the row's, or a security-class copy without its details
(they live in that copy's own row).

### Which partitions

The run verifies the monthly partitions whose **bounds as PostgreSQL holds them**
end at or before the cutoff (the first instant after the `-through` month, UTC). It
does not go by the month in a partition's name. A partition created from a session
in another time zone has bounds on that zone's midnights, so `audit_events_2026_08`
created from New York ends at 04:00 UTC on 1 September, after the cutoff of
`-through 2026-08`, so it is **not** in the run and is never dropped by it. Events
it holds from before the cutoff are then outside every verified partition, which
fails the run as described next.

Whatever the partitions are, the run also counts, by table, the events older than
the cutoff that **no verified partition covers**: in the default partition, in a
partition under another name, in a partition that straddles the cutoff, or in
`audit_events` itself when it is not partitioned. Any such event fails the
verification (`uncovered` in the receipt lists the table and the count), so a
mis-provisioned table cannot pass by having its odd corner left out of the plan. A
run with no partition to verify and nothing older than the cutoff elsewhere has
verified nothing; it reports **nothing to verify** (exit 5, `nothing_to_verify`)
and never `verified`.

### Copies are keyed by id

The copy and its read-back match events by `id` within a window, while the source
key is `(id, created_at)`. Two source rows may in principle share an id at different
instants. When both fall in the same UTC-day window, each is compared with both
stored copies, one differs from it, and verification fails closed with "stored
envelope differs from the row"; nothing is dropped. When they fall on different days
each copy matches its own row and verification passes, leaving two warehouse rows
with one id, as the source holds. Deployments that generate ids as UUIDs do not
produce either.

### Receipt and digest

A `codefly/audit-history/v1` receipt includes `deployment_id`, `sink`,
`configuration`, `through`, `observed_at`, `verified`, `partitions_sha256`,
`partitions`, `dropped`, `cutoff`, `uncovered`, `drop_refused`, and `error_code`
(empty on success). Each partition names `name`, `from`, `to`, per-organization
`orgs` counts, `events_sha256`, and `failures`. `uncovered` lists
`{table, rows}` for events outside the verified partitions. `drop_refused` is the
reason a confirmed drop did not happen. `error_code` is `verification_failed`,
`drop_refused`, `nothing_to_verify`, or `operation_failed`. Payloads and provider
error messages are excluded.

`configuration` identifies the destination without a credential: the BigQuery
project and dataset, or for ClickHouse `AUDIT_CLICKHOUSE_DATABASE` and
`AUDIT_CLICKHOUSE_CLUSTER` (empty when there is no cluster), plus the archive,
deployment and retention. It never carries a DSN, host or password.

`partitions_sha256` binds the deployment, cutoff, partition names and ranges,
per-organization counts, and a canonical source stream of every event envelope,
retention class and details hash. Run timing and the number copied by a particular
attempt are excluded, so successful copy and verification runs agree. Equal counts
with changed event content produce a different authorization digest.

### Removal

Removing partitions requires an explicit month, a verification-only pass, and
`-confirm-drop -expected-partitions-sha256 <digest-from-prior-verification>`.
Copy, verify and drop are separate operations: the confirming run verifies again,
compares the approved digest, and refuses before any DDL when the digest differs,
the cutoff has not passed, a verified partition changed or went away, or the
cutoff would reach a partition it did not verify.

The drop itself is one database call, `audit_events_drop_verified_partitions`, that
takes the **explicit list of verified partition names** with the number of rows
each held when verified. In one transaction it locks `audit_events` and each named
partition so no insert can route into one, confirms from the catalog that each is a
monthly partition of `audit_events` whose upper bound is at or before the cutoff,
recounts each and compares the count with the verified one, and only then drops
exactly those partitions. Any mismatch raises and rolls the transaction back with
nothing dropped. The command refuses unless the names the database returns are
exactly the verified ones, before the transaction commits, and the receipt's
`dropped` is what the database reported dropping, not what was planned. A writer
that arrives while the drop holds the locks waits, and is told there is no partition
for its row once the drop commits; a writer already in flight is waited for, and if it
commits, its row makes the recount disagree, so the drop is refused instead of
destroying the row. The
lock wait is bounded (10 seconds); a writer that does not finish is a refusal.

The older `audit_events_drop_partitions_before` (month taken from the partition
name, no recount) remains for retention and is not called by this command.

An unverified copy, missing confirmation, changed digest, or refusal drops nothing.
The operator must separately establish complete cutover acceptance, authenticate the
receipt and pin the running image, destination and pod identity before removal.

### Exit status of `audit-history-copy`

| Status | Meaning | Receipt `error_code` |
|---|---|---|
| 0 | done: copied and verified, and dropped when confirmed | empty |
| 1 | failed: configuration, connection, warehouse or database error | `operation_failed` and the other safe failure codes |
| 2 | usage: a flag or value is invalid (malformed, current or future `-through`; `-batch-size` outside 1 to the relay maximum; a drop without `-through`, `-verify-only` and the digest) | none (nothing is written) |
| 3 | verification failed: an event is missing or differs, or events older than the cutoff sit outside the verified partitions; nothing dropped | `verification_failed` |
| 4 | drop refused: the approval does not match, the cutoff has not passed, or what was verified no longer holds (non-JSON mode prints the reason); nothing dropped | `drop_refused` |
| 5 | nothing to verify: no partition ends at or before the cutoff and nothing older is held elsewhere; nothing claimed, nothing dropped | `nothing_to_verify` |

`audit-qualify` exits 0 when its probes passed, 1 when a probe or the destination
failed (a receipt with an `error_code` is written to standard output), and 2 for
invalid options.

## Bounded live qualification

A prepare phase is an explicit audit mutation. Run `./app audit-qualify -json
-prepare -run-id <unique UUID> -organization <existing UUID> -organization
<other existing UUID>`. It uses `RecordTx` under the queued mode to commit six
fixtures together: security and content events for both organizations and the
platform. No domain events or webhook jobs are published. If an organization is
absent, or Postgres created `audit_events` rows between the moment prepare started
and the end of the fixture transaction, the fixture transaction aborts. Use a fresh
run ID for every prepare attempt.

Verify with `-verify` and the same run ID and organizations, plus `-window-from`
and `-window-to` copied from the prepare receipt (the window must be recent: it
starts within the last 24 hours). `-timeout` defaults to two minutes
and cannot exceed five. Verify emits nothing and waits for the service's normal
relay; it does not create a second worker or interfere with a running relay.

The `codefly/audit-qualification/v1` receipt includes the destination/configuration,
run ID, observation time, five-minute event window, six event IDs/scopes/classes
and content hashes, and a `checks` map with `passed`, `failed` or `pending` values.
Successful probes verify every stored copy's envelope, class and details hash;
scoped list, aggregation, export and readable-source results; organization-scope
refusals; zero Postgres audit rows in the interval named below; and an actually
observed empty cell queue. Warehouse scans refuse more than 20,000 events in the
bounded window.

`postgres_no_new_rows` proves one thing and the receipt names it:
`postgres_checked_from` and `postgres_checked_to` bound the interval of
`audit_events.created_at` it read, `[from, to)`. Prepare starts the interval
(`from` is `window_from`) and checks it up to the end of its fixture transaction.
Verify checks from the same `window_from` to **its own run time**, whether that
falls inside the five-minute event window or long after it, so a verify run after
prepare covers everything created in between and a verify run inside the window
claims only the part that has happened. It does not prove that no row can exist: a
row whose transaction was still open at the check, or that was written with an
earlier explicit `created_at`, is outside what it read. The five-minute
`window_from`/`window_to` is the warehouse event window, not the Postgres interval.

`deployment_isolation`, archive object/lock enforcement, gateway authorization,
controlled relay interruption/recovery and alert delivery remain **pending** until
separate live evidence proves them. Seeing fixtures in the warehouse does not
prove those checks. A zero exit status says the command's probes passed; it does
not mean the full cutover is accepted or that history may be removed. Archive
observers should scan through `observed_at` as well as the event window, because
relay retries can compose archive objects after their events occurred.
