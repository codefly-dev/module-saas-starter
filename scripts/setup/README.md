# External-service setup scripts

These scripts configure real third-party adapters for a runnable Codefly
environment. They are operator tooling, not test harnesses.

## Contract

Every provider setup script must:

- write runtime configuration through the provider's generic Codefly
  configuration boundary;
- keep resolved credentials out of Git and give secret files mode `0600`;
- never accept secrets as command-line arguments or print them;
- validate credentials with a read-only provider request when supported;
- be safe to rerun and require `--force` before replacing different values;
- finish with `codefly doctor` and the exact remaining dashboard action;
- avoid installing provider SDKs or rewriting application source.

Provider-side mutations must be explicit. A script must not claim it created
redirects, webhooks, products, domains, or projects when the provider only
offers that operation through its dashboard.

## Why these are not Codefly services

A managed provider such as WorkOS has no local process or image for Codefly to
build, run, route, restart, or deploy. Modeling one as a normal service would
put a non-runnable node in the runtime graph and give `codefly run` misleading
lifecycle semantics.

Scripts are the current executable contract. After multiple provider
implementations establish the common lifecycle, they should be extracted into
a first-class Codefly external-integration capability with operations such as
`plan`, `apply`, `doctor`, configuration export, and drift inspection. A
service-agent technique is only documentation, while a secret provider only
resolves stored values; neither currently owns external SaaS provisioning.

## WorkOS

The WorkOS script configures the generic `identity` capability used by Accounts
and the frontend:

```bash
scripts/setup/workos.sh \
  --client-id client_01EXAMPLE \
  --api-key-file /secure/path/workos
```

It can also consume the `.env` block copied from the WorkOS application page:

```bash
scripts/setup/workos.sh --env-file /secure/path/workos.env
```

The environment file should contain:

```dotenv
WORKOS_CLIENT_ID=client_...
WORKOS_API_KEY=sk_...
```

The script configures authentication and reuses the same staging API key for
the optional WorkOS-backed SSO administration adapter. It validates the API key
and application JWKS without exposing either credential. It resolves
auth-sidecar's public endpoint through Codefly, exercises the hosted
authorization endpoint, and fails if that exact callback has not been
registered. It never writes the resolved port into runtime configuration.

If callback validation fails, add the URI printed by the script under the
WorkOS application's **Redirects** tab and rerun it. The WorkOS dashboard
currently owns that operation.

## Implemented provider stack

Each integration is independently selectable. Running one script also
materializes fail-closed defaults for the other optional capabilities, so the
`local-dogfood` environment remains complete and Codefly-valid.

| Script | Codefly capability | Runtime behavior |
|---|---|---|
| `stripe.sh` | `billing` | Checkout, customer portal, signed durable lifecycle webhook |
| `resend.sh` | `email` | Transactional outbox plus signed, replay-safe delivery events |
| `posthog.sh` | `product-analytics` | Browser/server events and person-deletion workflow |
| `sentry.sh` | `error-tracking` | Browser/server errors, traces, release correlation |
| `otel.sh` | `observability` | In-graph OTLP gateway with debug or OTLP/HTTP forwarding |
| `turnstile.sh` | `abuse-protection` | Registration and waitlist challenge verification |

Resolved files are written below `configurations/local-dogfood/`; public and
secret files are both owner-readable only. Public browser values are still
projected through Codefly's configuration model rather than read from a local
`.env` by Next.js.

### Stripe

Use a test-mode restricted key and an existing webhook secret:

```bash
scripts/setup/stripe.sh \
  --api-key-file /secure/path/stripe-api-key.env \
  --webhook-secret-file /secure/path/stripe-webhook-secret.env
```

For local delivery, keep Stripe CLI forwarding to the Codefly-owned callback.
Resolve the callback at runtime; do not copy its port into configuration:

```bash
stripe listen --forward-to \
  "$(codefly endpoint auth-sidecar --type rest)/v1/billing/webhook"
```

Put the `whsec_...` printed by that listener in the webhook secret file, run
the setup script, and keep the listener running while dogfooding.

Alternatively, expose the Codefly ingress through a public HTTPS tunnel and
explicitly let the script create the remote webhook:

```bash
scripts/setup/stripe.sh \
  --api-key-file /secure/path/stripe-api-key.env \
  --webhook-origin https://YOUR-TUNNEL.example \
  --provision-webhook
```

Remote provisioning deliberately rejects the loopback URL returned by
`codefly endpoint`: Stripe cannot deliver to localhost. The external tunnel
origin is provider-side ingress configuration; every local host and port behind
it remains Codefly-owned.

The script refuses live keys. Product names, pricing, metering, and tax choices
remain operator-owned decisions. Dogfood with Stripe test cards, then verify
checkout, portal return, duplicate webhook delivery, and out-of-order
subscription convergence.

### Resend

Configure a verified sender plus an existing signing secret:

```bash
scripts/setup/resend.sh \
  --api-key-file /secure/path/resend-api-key.env \
  --webhook-secret-file /secure/path/resend-webhook-secret.env \
  --from 'Example <onboarding@example.com>'
```

Resend cannot forward webhooks to localhost. Expose the Codefly ingress through
a public HTTPS tunnel, then provision that external origin explicitly:

```bash
scripts/setup/resend.sh \
  --api-key-file /secure/path/resend-api-key.env \
  --from 'Example <onboarding@example.com>' \
  --webhook-origin https://YOUR-TUNNEL.example \
  --provision-webhook
```

`--provision-webhook` rejects a loopback origin and explicitly creates the
delivery endpoint when absent. If the webhook already exists, Resend cannot
reveal its signing secret; pass the previously saved secret file instead.
The receiver verifies the exact raw request with Resend/Svix, rejects stale or
tampered events before persistence, deduplicates by `svix-id`, stores no raw
message or recipient PII, and projects invitation `sent`, `delivered`,
`bounced`, and `complained` states monotonically.

### PostHog

PostHog has separate capture and management origins. Supply both so regional
or self-hosted deployments do not accidentally send deletion requests to the
ingestion host:

```bash
scripts/setup/posthog.sh \
  --project-key-file /secure/path/posthog-project-key.env \
  --personal-key-file /secure/path/posthog-personal-key.env \
  --project-id 12345 \
  --host https://eu.i.posthog.com \
  --api-host https://eu.posthog.com
```

The project key may be exposed to the browser; the personal key never is.
Dogfood consent opt-in/withdrawal, anonymous-to-user identity, logout reset,
durable backend export, and privacy deletion.

### Sentry

Use a scoped organization token. With remote validation enabled, the script
validates the project and discovers its DSN:

```bash
scripts/setup/sentry.sh \
  --token-file /secure/path/sentry-token.env \
  --org example \
  --project saas-starter \
  --environment local-dogfood
```

Dogfood one controlled browser exception and one backend error. Confirm the
release/environment tags and browser-to-backend trace correlation. Provider
mode is explicit and fails closed on partial or conflicting configuration.

### OpenTelemetry

No external account is required for local inspection:

```bash
scripts/setup/otel.sh --debug
```

For a hosted observability backend:

```bash
scripts/setup/otel.sh \
  --endpoint https://otlp.example.com \
  --headers-file /secure/path/otlp-headers.env
```

Accounts resolves the collector dependency and its port exclusively through
the Codefly SDK. The in-graph gateway accepts OTLP gRPC traces, metrics, and
logs, then either prints a privacy-safe debug summary or forwards protobufs to
the configured OTLP/HTTP origin.

### Cloudflare Turnstile

Cloudflare's deterministic credentials make local dogfooding reproducible:

```bash
scripts/setup/turnstile.sh --fixture pass
scripts/setup/turnstile.sh --fixture fail --force
scripts/setup/turnstile.sh --fixture replay --force
```

For a real widget:

```bash
scripts/setup/turnstile.sh \
  --site-key 0x4AAAA... \
  --secret-file /secure/path/turnstile-secret.env \
  --hostnames localhost,app.example.com
```

The backend binds tokens to the exact action and hostname. Registration and
waitlist writes occur only after successful verification.

## Run and inspect

After configuring any subset:

```bash
codefly doctor workspace --env local-dogfood
codefly run service auth-sidecar --env local-dogfood
```

Use `codefly endpoint auth-sidecar --type rest --require-up` for the product
URL. Provider scripts resolve this endpoint themselves; generated ports and
URLs must never be copied into application configuration. WorkOS redirects
and Stripe CLI forwarding can use that loopback endpoint. A remote webhook
provider requires a separate public HTTPS tunnel/deployed ingress supplied
with `--webhook-origin`; the scripts refuse to pretend localhost is remotely
reachable.

Do not add scripts that inject raw shell environment variables directly into a
service. Introduce the generic Codefly capability first, then automate its
provider adapter.
