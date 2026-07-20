# Durable inbox/outbox contract

Status: generic foundation, producer path, worker runtime, operations surface,
and the Stripe, outbound-webhook, and email workload migrations ready.
`P2-JOB-001` through `P2-JOB-007` are complete; remaining workload migrations
are tracked as `P2-JOB-008` and `P2-JOB-009` in the root
[TODO](../TODO.md).

This contract is product-neutral. Warden, Codefly, Mind, and application
plugins use the same envelope and lifecycle; they identify their own queue,
topic, source, payload schema, and handler without changing the platform state
machine.

## Sources of truth

1. `services/accounts/proto/saas/jobs/v1/jobs.proto` owns the versioned wire
   vocabulary for directions, scopes, producer requests, structured ordering,
   enqueue outcomes, states, attempts, leases, failures, and transition records.
2. Codefly generates Go and TypeScript types under `saas/jobs/v1`.
3. `services/store/migrations/72_job_platform_contract.up.sql` owns the durable
   PostgreSQL representation and database authority boundary.
4. `services/accounts/code/pkg/jobs/state.go` defines legal transitions using
   the generated enum. Integration tests exhaustively compare that matrix with
   PostgreSQL's transition predicate.
5. `services/accounts/code/pkg/jobs/store.go` and `producer.go` own the
   transport-independent producer/worker interfaces, generated-command
   validation, ordering-key encoding, and request fingerprints.
6. `services/accounts/code/pkg/infra/postgres_job_producer.go` owns
   transaction-aware request and privileged enqueue adapters.
7. `services/accounts/code/pkg/infra/postgres_jobs.go` owns atomic PostgreSQL
   claim, heartbeat, retry, completion, dead-letter, and lease-recovery
   operations.
8. `services/accounts/code/pkg/jobs/worker.go` owns the reusable polling,
   heartbeat, failure-sanitization, metrics, tracing, retry, and shutdown loop.
9. `services/accounts/code/pkg/infra/postgres_job_operations.go` owns the
   payload-free cross-tenant operations projection and audited replay adapter.
10. `services/accounts/proto/saas/billing/v1/jobs.proto` is the first
    workload-owned generated payload contract; it demonstrates how products
    extend the generic envelope without changing its lifecycle vocabulary.
11. `services/accounts/proto/saas/webhooks/v1/jobs.proto` owns the exact-byte
    outbound-webhook workload consumed by the same generic runtime.
12. `services/accounts/proto/saas/notifications/v1/jobs.proto` owns the exact
    rendered transactional-email workload consumed by the generic runtime.

The workload contract is not a public Accounts RPC. Workload-specific services
adapt their payloads internally. Accounts exposes only a super-admin operations
projection whose generated response types cannot contain payload or attributes.

## Envelope model

`job_messages` stores both sides of the transactional-message pattern:

| Field | Contract |
| --- | --- |
| `direction` | `inbox` accepts an external event; `outbox` emits a durable side effect. |
| `scope_kind` | `tenant`, `subject`, or privileged-worker-only `global`. |
| `queue` | Worker pool and concurrency boundary. |
| `topic` | Versioned workload event/command name. |
| `source` | Stable producer identity. |
| `idempotency_key` | Required producer key; unique within direction, scope, queue, and source. |
| `request_fingerprint` | SHA-256 of the validated generated enqueue request; distinguishes an exact retry from conflicting key reuse. |
| `ordering_key` | Optional canonical encoding of a structured namespace and components; strict FIFO is enforced atomically while claiming. |
| `schema_version` | Positive payload schema version. |
| `payload` | Exact bytes, capped at 1 MiB. Larger artifacts live in object storage. |
| `attributes` | Bounded non-payload routing/diagnostic metadata. Never credentials. |
| `replay_of` | Optional immutable link to the terminal source job. |

Identity, routing, scope, payload, maximum attempts, and replay lineage are
immutable. A retry mutates lifecycle fields only. An operator replay creates a
new pending row with a new idempotency key and `replay_of`; it never rewrites
terminal history.

`job_attempts` stores one row per acquired lease. `(job_id, attempt_number)` and
`(job_id, lease_token)` are unique. `job_state_transitions` is append-only and
is written automatically by a security-definer trigger so callers cannot omit
or forge lifecycle history.

## State machine

```text
                 ┌───────────────┐
                 │   canceled    │
                 └──────▲────────┘
                        │
pending ──claim──> processing ──success──> succeeded
   │                    │
   └──cancel────────────┘
                        │
                        ├──retryable failure──> retrying ──claim──┐
                        │                                         │
                        └──permanent/exhausted──> dead_letter     │
                                                                  │
                                      <───────────────────────────┘
```

The exact legal edges are:

| From | To |
| --- | --- |
| `pending` | `processing`, `canceled` |
| `processing` | `retrying`, `succeeded`, `dead_letter` |
| `retrying` | `processing`, `canceled` |

`succeeded`, `dead_letter`, and `canceled` are terminal. Entering `processing`
increments `attempt_count` exactly once and requires owner, UUID fencing token,
expiry, and heartbeat. Leaving it clears all lease fields. Terminal timestamps,
failure metadata, attempt budgets, payload limits, and state version monotonicity
are database constraints rather than worker conventions.

## Authority model

- `app_tenant` has no direct rights on any job relation. Its only capability is
  `EXECUTE` on `enqueue_job_message`. That security-definer operation accepts
  outbox work only and independently verifies that tenant scope matches
  `app.current_org_id` or subject scope matches `app.current_user_id`. Forced
  insert RLS remains defense in depth. Request traffic cannot append global or
  inbox work, inspect payloads, claim, finalize, or write history.
- `app_job_worker` is `NOLOGIN`, `NOINHERIT`, and `BYPASSRLS`. It has only
  `SELECT/INSERT/UPDATE` on messages and attempts plus `SELECT` on transitions.
  It may execute enqueue for global/inbox producers and `replay_job_message`
  for dead-letter recovery, but cannot delete history or access product tables.
- `app_control_plane` has no job relation or lifecycle rights. Its one job
  capability is `EXECUTE` on `enqueue_job_message`, whose dedicated branch
  accepts global outbox work only. This permits a pre-authentication token row
  and its exact email command to commit together without granting payload reads,
  inbox receipt, tenant/subject spoofing, claim, finalization, or replay.
- `app_billing_worker` and `app_webhook_worker` receive no relation or
  enqueue-operation rights on the common platform. Stripe and
  outbound-webhook execution use `app_job_worker`; each product projection role
  is limited to its own product tables.
- The migration owner owns relations and protected trigger functions. Runtime
principals cannot create schema objects, connect directly, create temporary
relations, or assume one another's roles.

## Producer mechanics

`P2-JOB-003` is implemented behind the product-neutral `jobs.Producer`
interface. `EnqueueJobRequest`, `NewJob`, `JobOrderingKey`, and
`EnqueueJobResponse` are Codefly-generated from `saas.jobs.v1`; they are an
internal application contract and do not expose an Accounts RPC or HTTP route.

- Validation happens before a database call. Scope, direction, routing names,
  schema version, payload/attribute bounds, priority, attempt budget, and
  schedule all come from the generated contract.
- An ordering key is structured as a lowercase namespace plus one to eight
  opaque components. Each component is independently encoded with unpadded
  base64url before joining, so component boundaries cannot collide. The final
  database key is capped at 255 bytes and is stable across producers.
- The idempotency fingerprint is SHA-256 over a domain-separated,
  deterministic protobuf encoding of the complete validated enqueue request.
  Reusing the unique producer key with the same fingerprint returns the
  original job ID and `DUPLICATE`; different content returns
  `jobs.ErrIdempotencyConflict` without changing the stored row.
- Request-scoped producers must call `PostgresStore.EnqueueJob` inside the
  existing `WithOrgTx` or `WithUserTx` transaction. A bare context returns
  `jobs.ErrTransactionRequired`, preserving one commit boundary between the
  business mutation and its outbox message. The privileged worker producer
  opens its own short transaction for global or inbox ingestion.
- `enqueue_job_message` performs insert-or-resolve atomically inside PostgreSQL.
  Concurrent exact retries serialize on the unique idempotency index and return
  one inserted row plus the same durable identity to every duplicate caller.
  Absent attributes are persisted as an empty object, never JSON `null`.

Unit and fresh-PostgreSQL tests cover deterministic and collision-free ordering,
semantic fingerprints, exact duplicate resolution, conflicting key reuse,
concurrent retries, organization and subject binding, request direction/global
denial, control-plane global-outbox-only authority, raw-table denial, privileged
inbox enqueue, and rollback with the surrounding business transaction.

## Execution mechanics

`P2-JOB-002` is implemented behind the product-neutral `jobs.Store` interface.
Its inputs are the Codefly-generated `saas.jobs.v1` commands; it does not add an
Accounts RPC, HTTP route, product handler, or Warden-specific dependency.

- A claim is scoped to one queue and one bounded batch. PostgreSQL selects ready
  work with `FOR UPDATE SKIP LOCKED`, transitions each row to `processing`,
  issues a different UUID fencing token per job, and opens the matching attempt
  in the same transaction. The caller receives jobs only after commit.
- `available_at` is authoritative scheduling state. A worker computes
  `retry_at` from its bounded, configurable policy and submits it with a
  generated retry command; the generic store does not bake in product timing.
- A non-empty `ordering_key` is strict FIFO within its queue. A ready job cannot
  pass an older pending, processing, or scheduled-retrying predecessor. Jobs
  without a key retain normal priority and availability ordering.
- Heartbeats and all finalizers match job id, worker id, UUID token, processing
  state, and a lease that is still live according to the database clock. A
  wrong, superseded, or expired token returns `jobs.ErrLeaseLost` without
  mutation. Heartbeats cannot revive expired work.
- Claim polling closes expired attempts as `lease_expired`, then either makes
  the message immediately retryable or dead-letters it when the attempt budget
  is exhausted. A recovered attempt always receives a new token.
- Success, retryable failure, and permanent failure finalize the attempt and
  message together. Retry exhaustion deterministically becomes `dead_letter`;
  no late worker can overwrite that result.

Real-PostgreSQL tests cover unavailable schedules, concurrent replicas,
disjoint claims, unique tokens, strict ordering, heartbeat renewal, wrong and
expired fences, retry timing, attempt ledgers, crash recovery, late finalizers,
permanent failures, and both explicit and lease-expiry budget exhaustion.

## Worker runtime and operations

`P2-JOB-004` adds the reusable `jobs.Worker` above the persistence interface.
It polls one queue, claims bounded batches, runs handlers concurrently, renews
leases, and finalizes only with the current fence. Typed `ProcessingError`
values are the only handler diagnostics allowed into durable history. Arbitrary
errors and panics become stable generic codes, preventing payloads or
credentials from leaking through error text. The worker exposes atomic
process-local counters through generated `JobWorkerMetrics`; durable depth,
age, schedule, dead-letter, and expired-lease counts come from PostgreSQL and
use queue as their only metric dimension.

Every poll and job execution creates a Wool/OpenTelemetry span. Trace metadata
is limited to queue, topic, and job identity; payload, attributes, tenant,
subject, source, and failure text are not labels. `Shutdown(ctx)` stops new
claims, waits for already-claimed handlers, and cancels them only when the
caller's deadline expires. Unfinished work is recovered through lease expiry.

The existing `PlatformAdminService` exposes four generated, confidential,
super-admin operations:

- database-derived queue snapshots;
- seek-paginated payload-free job summaries;
- one job's safe metadata, attempts, and append-only state history;
- idempotent replay of dead-lettered work after mandatory recent MFA.

Replay copies payload and immutable routing fields entirely inside PostgreSQL,
creates a new pending row linked through `replay_of`, and never rewrites its
source. `replay_job_message` is executable only by `app_job_worker`. A
domain-separated deterministic fingerprint resolves exact retries and rejects
changed reuse of the same operator idempotency key. One success audit event is
emitted only when the replay row is first inserted.

The frontend route `/admin/platform/jobs` is cataloged as `super_admin`, mounts
behind a role gate, shows only the generated payload-free model, refreshes queue
health, filters and seek-pages jobs, displays attempt/transition history, and
offers replay only for dead letters. Both the request field and
`Idempotency-Key` transport header use the same browser-generated key.

Worker unit tests run with the race detector and cover success, typed retry and
permanent failure, untyped-error and panic redaction, heartbeat, metrics, and
deadline shutdown. Fresh-PostgreSQL tests cover operations snapshots,
pagination, lifecycle detail, worker-only replay authority, exact duplicate,
fingerprint conflict, non-dead-letter refusal, missing jobs, immutable lineage,
and server-side payload copying. Each existing workload migrates independently
through a generated payload adapter so moving one workload does not change the
delivery semantics of another.

## Stripe workload adapter

`P2-JOB-005` is the first complete workload migration. The signed public HTTP
endpoint still verifies Stripe's exact raw body before acknowledging delivery,
but now encodes the verified event as the Codefly-generated
`saas.billing.v1.StripeWebhookJob` protobuf. It enqueues one global inbox job
with queue `billing`, topic `stripe.webhook.process`, source `stripe.webhook`,
schema version `1`, Stripe event ID as its idempotency key, and the established
eight-attempt bounded retry policy. Exact retries resolve to the original job;
conflicting reuse of an event ID is rejected without replacing payload bytes.

The generic `jobs.Worker` owns claim, lease heartbeat, retry scheduling,
fencing, terminal state, safe failure history, metrics, traces, replay, and
graceful shutdown. A thin billing handler validates the immutable routing
contract, decodes and validates the generated payload, checks the inner event
ID against the outer idempotency key, and invokes the existing monotonic Stripe
projector. Malformed contracts are permanent safe failures; arbitrary provider
or projection errors remain retryable and are redacted by the worker.

Database authority is deliberately split: `app_job_worker` owns durable receipt
and job lifecycle, while `app_billing_worker` can only read billing catalogs and
project subscriptions across tenant RLS. The specialized Stripe queue table,
store methods, polling runtime, migrations, grants, and tests have been removed.
Fresh-PostgreSQL coverage proves signed HTTP receipt through generic enqueue,
claim, generated decoding, handler execution, and `succeeded` lifecycle state,
as well as the absence of the former specialized table.

## Outbound webhook adapter

`P2-JOB-006` moves audit-driven webhook delivery onto the same generic outbox.
`DurableAuditEmitter` inserts the audit event, one immutable delivery-history
row per matching subscription, and one generated `saas.webhooks.v1`
`OutboundWebhookJob` in the same organization transaction. A commit therefore
contains all three records or none. Each job uses queue `webhooks`, topic
`webhook.delivery.send`, source `saas.audit`, schema version `1`, the delivery
UUID as its idempotency key, and a structured `webhook_subscription` ordering
key containing the subscription UUID. Strict generic FIFO preserves per-endpoint
serialization across replicas.

The workload payload contains the delivery, subscription, and stable event
UUIDs, event type, and exact stored JSON bytes. A thin handler validates outer
routing, tenant scope, ordering, generated payload constraints, and equality
with immutable delivery history before invoking the existing SSRF-safe,
Vault-backed signing transport. Successful history projection makes a later
lease-recovery execution a no-op, reducing duplicate sends when generic job
completion is interrupted after projection. Endpoint/network failures update
the customer-visible latest outcome and remain untyped so the generic worker
redacts and retries them; malformed contracts and inactive subscriptions are
safe permanent failures.

`webhook_deliveries` is no longer a queue. It retains only exact request bytes,
latest HTTP outcome, attempt count, and customer-facing timestamps. Claims,
leases, schedules, maximum attempts, retry state, and dead letters exist only
in `job_messages` and its attempt/transition history. The specialized webhook
worker, queue store, lifecycle columns/indexes, and compatibility audit emitter
were removed. Migration 73 converges databases that already applied the older
specialized schema, while fresh installs receive the final shape directly.
Test and replay commands use this same transactional producer and worker path;
no synchronous HTTP executor remains.
`app_webhook_worker` can select subscriptions and delivery history, and update
only outcome/timestamp columns. It cannot rewrite routing or exact payload
bytes and has no generic job or unrelated product authority; `app_job_worker`
has the inverse boundary.

Unit coverage validates routing, exact-byte transmission, retry redaction,
permanent failures, bounded retry timing, already-projected idempotence, and
transactional test/replay enqueue.
Fresh-PostgreSQL coverage runs audit fan-out through generic claim and signed
HTTP delivery, checks product and job terminal state, proves projection/job
role separation, and asserts the specialized lifecycle columns are absent.

## Transactional email adapter

`P2-JOB-007` removes email transport from request and billing business paths.
The Codefly-generated `saas.notifications.v1.EmailDeliveryJob` retains the
validated recipients, sender, reply-to address, subject, exact rendered HTML
and text bodies, and bounded tags. Each command uses queue `notifications`,
topic `notification.email.send`, schema version `1`, and a recipient-digest
ordering key that does not expose an address in job metadata. Only the thin
email job handler invokes a provider.

Template lookup and rendering happen before enqueue. The renderer accepts only
the small `{{variable}}` language, rejects malformed or unresolved variables,
and contextually escapes values inserted into HTML. The rendered result—not a
mutable template reference—is the durable payload, so retries deliver the same
content even if a template changes. Built-in billing templates use the
server-owned subscription-management URL and do not route users to a pricing
page.

Invitation creation and its tenant-scoped email job share the same organization
transaction. Magic-link token insertion and its global email job share the
same audited pre-authentication control-plane transaction. A failure to enqueue
rolls back the corresponding product row. After Stripe projection, billing
uses the stable Stripe event/template identity to append a second email job;
enqueue failure is returned to the Stripe worker so its original event remains
retryable. Exact duplicate enqueue is accepted without creating another
delivery.

The generic worker owns email claims, leases, heartbeats, bounded retry,
dead-letter state, typed safe failures, telemetry, and shutdown. Provider
idempotency uses the durable job UUID: automatic retries keep the same key,
while an intentional operator replay receives a new provider key and can send
again while preserving the copied exact payload. Provider bodies and arbitrary
errors are never retained in job history.

In-app notifications remain direct durable destination rows with owner-bound
RLS; there is no external side effect to enqueue. Preference-based fan-out to
email, Slack, and other future channels remains a separate notification-product
slice and must reuse this platform rather than introduce another queue.

Unit tests cover generated exact payloads, strict and HTML-safe templates,
retry/permanent provider classification, exact duplicate enqueue, and replay
identity. Fresh-PostgreSQL tests prove tenant and pre-auth authority, atomic
invitation and magic-link rows, generic worker delivery, exact function ACLs,
and rollback on enqueue failure.
