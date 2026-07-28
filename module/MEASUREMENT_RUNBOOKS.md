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
