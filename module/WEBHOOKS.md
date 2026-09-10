# Outbound webhook contract

Webhook endpoints must be public `https://` URLs on port 443. Accounts resolves
all A/AAAA records when an endpoint is registered and again immediately before
each connection. If any answer is loopback, private, link-local, multicast,
metadata, cluster, documentation, benchmark, or otherwise special-use, the
request is rejected. Redirects are returned as failed attempts and never
followed. Kubernetes egress policy independently denies the same IPv4/IPv6
ranges.

This follows the application- and network-layer defense-in-depth guidance in
the [OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html).

An outbound webhook is a subscriber kind of the
[domain event contract](./EVENTS.md). An endpoint registered for N event names
is N `event_subscriptions` rows with `delivery = webhook`, bound to the
registering organization and to the registration itself, and the relay is the
only thing that fans an event out. Only a `visibility: external` type is
eligible; the audit registry declares every one of its types that way, so an
endpoint can subscribe to any registered audit event type as before.

The registration remains what a customer edits; its subscriptions are derived
from it in the same transaction, and deleting it cascades them away. Everything
below the fan-out is unchanged — the same `webhook_deliveries` row, the same
signed bytes, the same dispatch job — so this document's delivery contract holds
across the cutover.

What produces the event is the audit emitter: each org-scoped audit record
publishes its external domain event in the same transaction, and the relay
resolves the endpoints subscribed to that type after commit. The audit spine is
not the delivery channel; the event published beside the record is.

## Secret lifecycle

- Creation generates a 256-bit `whsec_...` key on the server, encrypts it with
  a subscription-bound Vault Transit envelope, and returns plaintext only in
  that creation response.
- List/get responses never contain the key.
- Rotation returns the new key once. For the requested overlap (the UI uses 24
  hours; the API permits at most seven days), deliveries contain signatures
  from both the new and prior keys. After the expiry, only the new key signs.
- Startup encrypts legacy plaintext keys before serving traffic. Legacy rows
  with an empty key are assigned an encrypted replacement and disabled because
  no consumer could have possessed that replacement.

## Request and signature

Every request is a `POST` with the exact persisted JSON bytes and these headers:

Subscription event names are canonical routing identifiers: 1–128 bytes,
starting with a lowercase letter and containing only lowercase letters, digits,
dots, underscores, or hyphens. This keeps `X-Webhook-Event` safe and portable.

Fan-out is scoped to the event's own tenant: the relay resolves subscriptions
across every tenant with RLS bypassed, so the subscription's `org_id` — not the
type pattern, and not the policy — is what keeps a security mutation whose
transaction runs with RLS bypassed (platform-admin and other control-plane
writes) from reaching another tenant's endpoints. A NULL-org audit record
publishes no event and so has no fan-out at all.

Fan-out routes on the audit event type, which is namespaced
(`<namespace>.<aggregate>.<event>`, this module minting only `saas.*` — issue
#520), so a subscription only ever fires for a namespaced name. The routing rule
above stays deliberately looser than the audit registry's: subscription names are
customer-facing identifiers with their own compatibility story, so a name that
matches no event is accepted and simply never fires. Migration 116 rewrote stored
subscriptions that named a registered event, so an existing subscriber kept
firing across the cutover; a subscriber that re-creates a subscription from a
hardcoded pre-#520 name will not.

```text
Content-Type: application/json
User-Agent: Codefly-Webhook/1.0
X-Webhook-Event: saas.user.created
X-Webhook-Event-ID: <stable event UUID>
X-Webhook-Delivery-ID: <attempt-history UUID>
X-Webhook-Signature: t=<unix-seconds>,v1=<hex-hmac>[,v1=<old-key-hex-hmac>]
```

The signed bytes are exactly:

```text
<unix-seconds>.<event-id>.<raw HTTP request body>
```

Verification order:

1. Read and retain the raw request body. Do not parse/re-serialize before
   verification.
2. Parse `t` and every `v1` value. Reject timestamps outside your replay
   tolerance; five minutes is the recommended default.
3. Compute HMAC-SHA256 for each currently accepted secret and compare digests
   in constant time. Accept when any expected digest matches any `v1` value.
4. Deduplicate atomically on `X-Webhook-Event-ID`, then process the JSON. Store
   the event ID before returning success so retries cannot apply the event
   twice.
5. Return a `2xx` only after the event is durably accepted. Every other status,
   redirect, timeout, or network error is retryable until the generic job's
   attempt budget is exhausted.

Manual replay creates a new delivery ID but preserves the event ID. Consumers
that already completed that event should return `2xx` without applying it again.

## Delivery lifecycle

Audit event insertion and the domain event that carries it commit in one
organization transaction; the relay then writes the delivery-history row and the
generated `saas.webhooks.v1.OutboundWebhookJob` message together in its own. Each job has the delivery UUID as its idempotency key and the
subscription UUID as a structured ordering key. The generic job platform uses
`FOR UPDATE SKIP LOCKED`, heartbeats, expiring fenced leases, and strict FIFO to
permit many replicas without concurrent delivery to one endpoint. Abandoned
leases recover after restart; failures use bounded backoff and terminal state
is visible in `/admin/platform/jobs`.

`webhook_deliveries` is customer-visible history, not a queue. It stores exact
request bytes, latest attempt outcome, and delivery timestamps. Generic job
records exclusively own schedules, attempt budgets, retry state, and dead
letters. `app_job_worker` owns that lifecycle; the isolated
`app_webhook_worker` role can only read endpoint configuration and update
delivery history. Test and manual-replay RPCs also atomically create a pending
delivery row plus a generated outbox job; they never perform inline HTTP.
Replay preserves the stable event ID and exact body instead of mutating prior
history, while the worker resolves the subscription's current endpoint and
signing key at execution time.

## Administration audit trail

Creating a subscription, deleting one, replaying a delivery, and rotating a
signing secret each commit a `saas.webhook.*` audit event in the mutation's own
transaction. The event names the caller who ran it:

- `actor_id` is the verified initiator resolved from the authenticated request,
  never the organization the change was made in.
- `actor_type` is the credential the caller presented — `user` for an
  interactive session, `api_key` for a key — as the auth perimeter reports it in
  `X-Credential-Kind`. It is not inferred from the caller's scopes: scopes are a
  key's authorization ceiling, and a key created without any carries none, so a
  scope set answers "was this constrained", never "was this a machine
  credential". A request whose credential kind the perimeter did not report is
  refused, not attributed to a guessed kind. `system` is reserved for genuinely
  automated work; no caller of these four operations claims it today.
- `delegated_by` (payload) lists every party that called on the initiator's
  behalf (the RFC 8693 `act` chain), immediate delegate first, and is absent on
  a direct call. It is the whole chain, not its head: the chain exists only in
  the request's token, so a hop left out here can never be recovered. It is
  delegation, not impersonation: the initiator stays the subject.

Nothing about the endpoint travels in the payload — not the signing secret, not
the one-time reveal, not the endpoint's query string.

These four events are at **schema version 2**. A version 1 row recorded
`actor_id` as the organization and `actor_type` as `system`; a version 2 row
records the initiating user and the credential they used. The event type names
are unchanged — a published type is immutable — so subscriptions keep firing,
and a subscriber reads the same values from the delivery payload's
`data.actor_id` and `data.actor_type`, where a consumer that treated `actor_id`
as an organization id now sees a user id.

`data.schema_version` is what tells the two contracts apart, in the audit table
and in the delivery envelope alike; every delivery now carries it, for every
event type. Branch on it rather than on a deployment date. Rows written before
this change are left exactly as they were recorded — an append-only trail is
not rewritten with a guessed actor — and their version, not a release note, is
what identifies them.

Impersonation is not yet part of this record. The accounts service does not
resolve an effective subject from the forwarded impersonation assertion at all,
so no webhook event can carry one; closing that is the shared identity work in
issue #533, and these events pick it up from the same contract when it lands.
