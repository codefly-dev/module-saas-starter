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
month into the warehouse and archive. Copy is resumable; existing copies are
verified before missing rows are appended. `-verify-only` writes nothing.

A `codefly/audit-history/v1` receipt includes `deployment_id`, `sink`,
`configuration`, `through`, `observed_at`, `verified`, `partitions_sha256`,
`partitions`, `dropped`, `cutoff`, and `error_code` (empty on success). Each
partition names `name`, `from`, `to`, per-organization `orgs` counts,
`events_sha256`, and `failures`. Payloads and provider error messages are excluded.

`partitions_sha256` binds the deployment, cutoff, partition names and ranges,
per-organization counts, and a canonical source stream of every event envelope,
retention class and details hash. Run timing and the number copied by a particular
attempt are excluded, so successful copy and verification runs agree. Equal counts
with changed event content produce a different authorization digest.

Removing partitions requires an explicit month, a verification-only pass, and
`-confirm-drop -expected-partitions-sha256 <digest-from-prior-verification>`.
The copier verifies again and compares the approved digest before any DDL. It also
rechecks the source partition set and counts in the control-plane drop transaction.
An unverified copy, missing confirmation or changed digest drops nothing. The
operator must separately establish complete cutover acceptance, authenticate the
receipt and pin the running image, destination and pod identity before removal.

## Bounded live qualification

A prepare phase is an explicit audit mutation. Run `./app audit-qualify -json
-prepare -run-id <unique UUID> -organization <existing UUID> -organization
<other existing UUID>`. It uses `RecordTx` under the queued mode to commit six
fixtures together: security and content events for both organizations and the
platform. No domain events or webhook jobs are published. If an organization is
absent, or Postgres receives rows in `audit_events` in the qualification window,
the fixture transaction aborts. Use a fresh run ID for every prepare attempt.

Verify with `-verify` and the same run ID and organizations, plus `-window-from`
and `-window-to` copied from the prepare receipt. `-timeout` defaults to two minutes
and cannot exceed five. Verify emits nothing and waits for the service's normal
relay; it does not create a second worker or interfere with a running relay.

The `codefly/audit-qualification/v1` receipt includes the destination/configuration,
run ID, observation time, five-minute event window, six event IDs/scopes/classes
and content hashes, and a `checks` map with `passed`, `failed` or `pending` values.
Successful probes verify every stored copy's envelope, class and details hash;
scoped list, aggregation, export and readable-source results; organization-scope
refusals; zero Postgres audit rows in the window; and an actually observed empty
cell queue. Warehouse scans refuse more than 20,000 events in the bounded window.

`deployment_isolation`, archive object/lock enforcement, gateway authorization,
controlled relay interruption/recovery and alert delivery remain **pending** until
separate live evidence proves them. Seeing fixtures in the warehouse does not
prove those checks. A zero exit status says the command's probes passed; it does
not mean the full cutover is accepted or that history may be removed. Archive
observers should scan through `observed_at` as well as the event window, because
relay retries can compose archive objects after their events occurred.
