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

```text
Content-Type: application/json
User-Agent: Codefly-Webhook/1.0
X-Webhook-Event: user.created
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

Audit event insertion, matching delivery-history rows, and generated
`saas.webhooks.v1.OutboundWebhookJob` messages commit in one organization
transaction. Each job has the delivery UUID as its idempotency key and the
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
