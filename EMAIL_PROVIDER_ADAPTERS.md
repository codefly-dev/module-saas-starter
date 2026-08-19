# Email provider adapters — investigation & proposed design

> **Status: investigation, not implementation.** This document answers the
> questions in issue #186 against the current tree and proposes a design
> (interface shape, capabilities descriptor, registry, Gmail-DWD adapter
> sketch) *before* any adapter code ships. No behavioural code changes land
> with it. Every claim is grounded in a citation; where the honest answer
> contradicts the issue's framing, it says so.

## The question

Production email is **Gmail-based** (domain-wide delegation, as in
`obin-platform`). The saas-starter ships **Resend only**. We want a clean seam
so the transport is swappable — Gmail API, Resend, SMTP — without touching
triggers, templates, dedup, or authz, and we want each adapter to *declare what
it supports* rather than forcing a lowest-common-denominator interface.

## Current state — what's already abstracted

The transport is already behind an interface. The relevant surface:

- **`Sender` interface + `Message`** —
  `module/services/accounts/code/pkg/email/sender.go:75`. `Send(ctx, *Message)
  (id, error)`. `Message` already carries `IdempotencyKey`, `Tags`, and a
  `Validate()` boundary check.
- **Implementations** — Resend HTTP (`pkg/email/resend.go`), `LogSender` (dev)
  and `FakeSender` (tests) in `pkg/email/fake.go`.
- **Provider selection** — a hardcoded switch on `EMAIL_PROVIDER` (`log` |
  `resend`) that **fails closed**: it refuses to boot if Resend secrets are
  present while the provider is `log`, and refuses `resend` without both
  `RESEND_API_KEY` and `RESEND_WEBHOOK_SECRET`
  (`work.go:1208`, `configuredEmailSender`).
- **Delivery is durable** — the business layer never calls `Send`. It renders a
  template and enqueues an `EmailDeliveryJob` (`Outbox.Enqueue`,
  `pkg/email/jobs.go:100`); the generic job worker's handler
  (`NewJobHandler`, `jobs.go:200`) is the **only** caller of the adapter, with
  5 attempts and a 5s→30m backoff (`DeliveryRetryDelay`, `jobs.go:267`).
- **Delivery tracking** — a Resend/Svix webhook (`pkg/email/resend_webhook.go`)
  verifies the signature, projects a privacy-minimized event into
  `email_delivery_events`, and advances `invitations.delivery_status`
  (`migrations/84_resend_delivery_events.up.sql`). The webhook route is
  registered **only when `EMAIL_PROVIDER=resend`** (`work.go:451`).
- **Notification policy** — channels (`in_app`, `email`) and categories
  (product/marketing/digest = opt-out; security/billing = mandatory) live in
  `pkg/business/notification_policy.go`. This layer decides *whether* to send,
  never *how*, and never names a provider.

**The gap the issue names is real:** the `Sender` interface is transport-only.
There is no feature discovery, and provider selection is a static config switch
with no registry. Nothing today can answer "does the active provider support
idempotency keys / delivery webhooks?"

## Checklist findings

### 1. Adapter contract — grow `Sender`, or wrap it?

**Recommendation: don't grow `Sender`. Add a `Provider` that embeds it.**

`Send` is the hot path and the thing every implementation must get right;
keeping it a one-method interface keeps `FakeSender`/`LogSender` trivial and
keeps the worker's inner loop unchanged. Feature discovery is orthogonal to
sending, so it belongs on a wrapper the *wiring* reads, not the *worker's inner
loop*:

```go
// pkg/email/provider.go (proposed)
type Provider interface {
    Sender
    Name() string                 // "resend", "gmail", "log"
    Capabilities() Capabilities
}
```

`FakeSender`/`LogSender` gain a two-line `Name()`/`Capabilities()` and become
`Provider`s for free; `ResendSender` likewise. The worker keeps depending on the
narrow `Sender` (it only sends); `work.go` depends on `Provider` (it decides
what else to wire). This is the smallest change that closes the discovery gap.

### 2. Capabilities descriptor

A plain value struct — no behaviour, cheap to pass around, trivially
constructed by each adapter:

```go
type Capabilities struct {
    IdempotencyKeys  bool // provider dedups retried sends by key
    DeliveryWebhooks bool // provider emits delivery/bounce/complaint signals
    BatchSend        bool
    ProviderTemplating bool // false ⇒ we render then persist (our model)
    VerifiedSenderRequired bool
    Tagging          bool // analytics key/values on the send
}
```

| Capability | Resend | Gmail (DWD) | Log/Fake |
|---|---|---|---|
| IdempotencyKeys | ✅ header | ❌ | n/a |
| DeliveryWebhooks | ✅ Svix | ❌ | ❌ |
| BatchSend | ✅ | ❌ (per-message) | n/a |
| ProviderTemplating | ❌ (we render) | ❌ (we render) | ❌ |
| VerifiedSenderRequired | ✅ domain | ✅ workspace mailbox | ❌ |
| Tagging | ✅ (≤10) | ❌ | ❌ |

The descriptor is a **branch key for wiring**, not a runtime toggle on the send
path. Only two consumers actually need it today (§5, §6); the rest of the matrix
is documentation of provider differences, not code that must exist now.

### 3. Registry to replace the `EMAIL_PROVIDER` switch

Replace the hardcoded switch with a name→constructor registry. It keeps the
fail-closed guarantees that `configuredEmailSender` already enforces (never
silently downgrade to a log sink because one secret is missing) while making
"add a provider" a one-line registration instead of a new `case`:

```go
type Factory func(ctx context.Context) (Provider, error)

var registry = map[string]Factory{} // "resend", "gmail", "log"

func Register(name string, f Factory) { registry[name] = f }

func Select(ctx context.Context, name string) (Provider, error) {
    f, ok := registry[strings.ToLower(strings.TrimSpace(name))]
    if !ok {
        return nil, fmt.Errorf("EMAIL_PROVIDER must be one of %v", keys(registry))
    }
    return f(ctx)
}
```

Each factory keeps its own fail-closed secret validation (the Resend factory
still demands key + webhook secret; the `log` factory still refuses to boot with
Resend secrets present). The registry is a lookup, not a policy relaxation.

### 4. Idempotency — **does DB `ON CONFLICT` fully cover a no-key provider? No — and this matters.**

This is the checklist item most worth getting right, because the issue's phrasing
("confirm the DB `ON CONFLICT` dedup fully covers us") invites a wrong answer.
There are **two distinct duplication windows**, and the DB covers only one:

1. **Enqueue-time duplication** — a trigger fires twice and tries to enqueue the
   same delivery. `enqueue_job_message`'s `ON CONFLICT DO NOTHING` on
   `(direction, scope, queue, source, idempotency_key)`
   (`migrations/75_email_job_convergence.up.sql:115`) collapses this to **one
   job**. ✅ Fully covered, provider-independent. Gmail is fine here.

2. **Send-time retry duplication** — the worker calls `Send`, the provider
   *accepts and sends the mail*, but the worker crashes / times out before the
   job is acked. The generic worker then retries the same job, and
   `NewJobHandler` calls `Send` **again** with the same `Message`
   (`jobs.go:224`). With Resend, `IdempotencyKey` (the job UUID, set at
   `jobs.go:212`) is sent as the `Idempotency-Key` header (`resend.go:108`) and
   Resend dedups → **one email**. With Gmail there is no such key, so the second
   `Send` produces a **second real email**. ❌ **Not covered by the DB.**

So the honest conclusion: the DB `ON CONFLICT` guarantees *at-most-one job*, not
*at-most-one delivery*. A no-idempotency-key provider is **at-least-once** with a
genuine (small) duplicate-email risk confined to the crash-after-send window.

**Options, in order of preference:**

- **(A) Accept it, document it.** Transactional email double-send on a rare crash
  window is low-harm and is exactly the semantics obin-platform already lives
  with in production. Record `IdempotencyKeys: false` in the capabilities so the
  behaviour is explicit and discoverable rather than a silent surprise. This is
  the recommended default.
- **(B) Shrink the window** by acking the job in the same transaction that
  records the send — not available here, because the send is an external side
  effect the DB can't be transactional with. Not worth building.
- **(C) Provider-side pseudo-idempotency** — Gmail has none
  (`users().messages().send` has no idempotency parameter), so this is not
  available.

Recommendation: **(A)**. The capability flag turns "silent double-send" into "a
declared property of the Gmail adapter."

### 5. Delivery tracking — graceful degradation for a no-webhook provider

The webhook pipeline is intrinsically Resend-shaped:
`email_delivery_events.provider` has a `CHECK (provider = 'resend')`
(`migrations/84_resend_delivery_events.up.sql:11`), and `invitations.delivery_status`
only advances when a verified webhook lands. Initial status is `disabled`/`queued`
(`migrations/83_...:13`); nothing else moves it.

For Gmail (`DeliveryWebhooks: false`) the graceful-degradation story is: **the
adapter simply never writes delivery events.** `delivery_status` stays at its
enqueue value and never reaches `delivered`/`bounced`/`complained`. That is
correct, not broken — Gmail gives us no signal, so we promise none. Two concrete
consequences to make explicit:

- **Wiring:** the webhook route registration at `work.go:451` should key off
  `provider.Capabilities().DeliveryWebhooks`, not `EMAIL_PROVIDER=="resend"`.
  Same behaviour today, but it stops being a magic string and starts being a
  capability read.
- **UI/product:** any surface that shows delivery state should treat "no
  progression past `sent`" as "unknown", not "failed", when the active provider
  has no webhooks. No schema change needed; the `CHECK (provider='resend')` can
  stay until a second webhook-capable provider actually arrives (don't widen it
  speculatively).

### 6. Does the worker need to branch on capabilities? — barely, and that's good.

`NewJobHandler` always sets `IdempotencyKey` from the job UUID (`jobs.go:212`).
A no-key adapter simply **ignores** the field, so the worker's inner send loop
needs **no** capability branch — the adapter absorbs the difference. The only
places capabilities change behaviour are outside the hot loop:

- webhook-route registration (§5), and
- the `DeliveryError.Retryable` classification, which is already the adapter's
  job (`resend.go:122`) and stays so — Gmail's adapter maps its own 4xx/5xx to
  `Retryable` the same way.

This is the payoff of the wrapper design in §1: the worker stays capability-blind
and keeps working against the narrow `Sender`; only `work.go` reads capabilities.

### 7. Notification-system integration — nothing to change, and that's the point

`notification_policy.go` decides *whether* a channel/category is delivered and
never references a provider (`EvaluateNotificationDelivery`,
`notification_policy.go:46`). The `Outbox` renders and enqueues; the worker
sends. The transport swap happens entirely below the policy layer. **Confirmed
transport-agnostic** — no changes to channels, categories, policy, templates,
dedup, or the in-app `NotificationService` are required to add Gmail. This is
the invariant the design must preserve, and the wrapper/registry approach does.

### 8. Gmail (DWD) adapter sketch

Mirrors obin-platform's impersonation auth: a service account with domain-wide
delegation mints a short-lived token for `subject=<mailbox>`, scope
`https://www.googleapis.com/auth/gmail.send`, and calls `users().messages().send`
with an RFC 2822 MIME message built from the same `*Message`.

```go
// pkg/email/gmail.go (sketch — not shipped)
type GmailConfig struct {
    ServiceAccountJSON []byte // sender SA credentials
    Subject            string // mailbox to impersonate, e.g. no-reply@acme.com
}

type GmailSender struct{ svc *gmail.Service }

func NewGmailSender(ctx context.Context, cfg GmailConfig) (*GmailSender, error) {
    jwtCfg, err := google.JWTConfigFromJSON(cfg.ServiceAccountJSON, gmail.GmailSendScope)
    if err != nil { return nil, err }
    jwtCfg.Subject = cfg.Subject // domain-wide delegation
    svc, err := gmail.NewService(ctx, option.WithTokenSource(jwtCfg.TokenSource(ctx)))
    if err != nil { return nil, err }
    return &GmailSender{svc: svc}, nil
}

func (g *GmailSender) Send(ctx context.Context, m *Message) (string, error) {
    if err := m.Validate(); err != nil { return "", err }
    raw := encodeMIME(m) // From/To/Reply-To/Subject + multipart text+html
    out, err := g.svc.Users.Messages.Send("me", &gmail.Message{
        Raw: base64.URLEncoding.EncodeToString(raw),
    }).Context(ctx).Do()
    if err != nil { return "", classifyGmailError(err) } // → *DeliveryError
    return out.Id, nil // Gmail message id, stored like Resend's
}

func (g *GmailSender) Name() string { return "gmail" }
func (g *GmailSender) Capabilities() Capabilities {
    return Capabilities{VerifiedSenderRequired: true} // no idempotency, no webhooks
}
```

`classifyGmailError` maps Gmail's `googleapi.Error` codes to
`DeliveryError{Retryable: …}` (429/5xx retryable; 4xx permanent), reusing the
exact classification contract the worker already understands. `IdempotencyKey` on
the `Message` is silently ignored — declared truthfully by
`Capabilities().IdempotencyKeys == false`.

### 9. Config & secrets per provider

- **Resend** (today): `RESEND_API_KEY`, `RESEND_WEBHOOK_SECRET`,
  optional `RESEND_API_BASE`.
- **Gmail**: the sender **service-account JSON** (Vault, like the Ed25519
  signing key at `work.go:1246`) + the **impersonated mailbox** (`GMAIL_SUBJECT`
  or config) + the workspace-side DWD grant (Terraform, mirroring
  obin-platform's `iam.tf`; infra, not code in this repo).

Each factory validates its own secrets and fails closed — no partial-config
silent downgrade, matching the existing `configuredEmailSender` posture.

### 10. Tests

- **Capability matrix test** — table asserting each registered provider's
  `Capabilities()` (guards against a new adapter forgetting to declare, e.g.,
  `IdempotencyKeys`).
- **`FakeSender` as `Provider`** — add `Name()`/`Capabilities()` and a
  configurable capability set so worker/wiring tests can exercise both the
  webhook-capable and no-webhook branches without a real provider.
- **Registry** — unknown name fails closed; each factory rejects missing secrets
  (extends `work_provider_test.go`).
- **Idempotency semantics** — a worker-retry test showing the double-`Send` on a
  no-key adapter (documenting §4 option A as intended behaviour), and that a
  key-capable adapter is invoked with a stable key across retries.
- **Gmail MIME encoding** — `encodeMIME` round-trips headers and multipart body
  (pure, no network); error classification maps sample `googleapi.Error`s to the
  right `Retryable`.

## Proposed design — summary

1. `Provider = Sender + Name() + Capabilities()`; `Capabilities` is a plain
   value struct. Worker keeps depending on `Sender`; `work.go` depends on
   `Provider`.
2. Name→factory **registry** replaces the `EMAIL_PROVIDER` switch, preserving
   fail-closed secret validation.
3. Webhook-route wiring and delivery-status expectations branch on
   `Capabilities().DeliveryWebhooks`, not a provider string.
4. Gmail-DWD adapter: SA impersonation + `messages().send`, `IdempotencyKeys:
   false`, `DeliveryWebhooks: false`; accepts at-least-once delivery (§4-A).
5. Notification policy, templates, dedup, and in-app service are untouched —
   the swap lives entirely below the policy layer.

## Phasing (when implementation is greenlit)

1. **Seam** — add `Provider`/`Capabilities`, make existing senders implement it,
   introduce the registry, move webhook wiring onto `DeliveryWebhooks`. No
   behaviour change; Resend keeps working identically.
2. **Gmail adapter** — land `pkg/email/gmail.go` + factory + Vault secret load +
   Terraform DWD grant; select via `EMAIL_PROVIDER=gmail`.
3. **Docs/UI** — teach the delivery-status surface that "no webhooks ⇒ unknown,
   not failed" for no-webhook providers.

## Open decisions for review

- Confirm **§4 option A** (accept at-least-once for Gmail) is acceptable for
  production transactional mail, or whether the crash-after-send window warrants
  more than a documented capability flag.
- Whether to widen `email_delivery_events.provider` beyond `'resend'` now, or
  leave the `CHECK` until a second webhook-capable provider actually lands
  (recommendation: leave it — no speculative schema).
