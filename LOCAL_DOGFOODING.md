# Local SaaS dogfooding

This profile runs the SaaS starter as an actual local product. It is not a
test suite: Codefly starts the product ingress, frontend, Accounts, PostgreSQL,
Vault, cache, object storage, durable jobs, and the integration adapter for the
configured external identity provider.

## Runtime composition

Two Codefly inputs are intentionally independent:

| Input | Authority | Purpose |
|---|---|---|
| `--env local-dogfood` | `configurations/local-dogfood/*` | Select independently configured identity, billing, email, analytics, error, telemetry, and abuse adapters |
| optional `--fixture <name>` | `module/fixtures/<name>.yaml` | Seed a useful starting product state |

A fixture never changes the identity provider. The ordinary `local`
configuration selects `IDENTITY_PROVIDER=fixture`; the `local-dogfood`
configuration selects `IDENTITY_PROVIDER=workos`. This allows a real WorkOS
login with either an empty database or an optional seeded product scenario.

The module services depend only on the generic Codefly workspace
configuration named `identity`. WorkOS is one adapter for that capability;
the onboarding application library does not import or select WorkOS.

Auth-sidecar reads its own public REST endpoint through the Codefly SDK and
stamps that origin onto trusted upstream requests. Accounts uses the verified
origin for OAuth callbacks, WebAuthn, email links, and request-driven billing
redirects. The optional Codefly `application` origin remains only as a
production/background fallback; a local port never belongs in product config.

## Configure providers through Codefly

WorkOS is the identity adapter. The optional production-grade dogfood stack
also includes Stripe, Resend, PostHog, Sentry, the in-graph OpenTelemetry
gateway, and Cloudflare Turnstile:

```bash
scripts/setup/workos.sh --env-file /secure/path/workos.env
scripts/setup/stripe.sh \
  --api-key-file /secure/path/stripe.env \
  --webhook-secret-file /secure/path/stripe-webhook.env
scripts/setup/resend.sh \
  --api-key-file /secure/path/resend.env \
  --webhook-secret-file /secure/path/resend-webhook.env \
  --from 'Example <onboarding@example.com>'
scripts/setup/posthog.sh \
  --project-key-file /secure/path/posthog-project.env \
  --personal-key-file /secure/path/posthog-personal.env \
  --project-id 12345 \
  --host https://eu.i.posthog.com \
  --api-host https://eu.posthog.com
scripts/setup/sentry.sh \
  --token-file /secure/path/sentry.env \
  --org example \
  --project saas-starter
scripts/setup/otel.sh --debug
scripts/setup/turnstile.sh --fixture pass
```

Every script is independent, secret-safe, idempotent, and finishes with the
Codefly doctor. Provider-side creation is opt-in where supported. See
[`scripts/setup/README.md`](./scripts/setup/README.md) for exact requirements,
safe provisioning flags, and per-provider acceptance checks.

The product callback address always comes from
`codefly endpoint auth-sidecar --type rest`. WorkOS and the browser can use its
loopback URL directly. Stripe CLI can forward to it. Resend and remotely
registered Stripe webhooks need a public HTTPS tunnel or deployed ingress;
pass only that external origin with `--webhook-origin`. The scripts reject
remote provisioning against localhost, while Codefly continues to own every
internal host and port.

### WorkOS

See the product-ingress URL before starting it:

```bash
codefly endpoint auth-sidecar --type rest
```

Append `/auth/callback` and register that exact URI in the WorkOS staging
application. WorkOS staging accepts localhost callback URIs. Do not copy this
port into the starter configuration: Codefly owns endpoint allocation.

The recommended path is the safe setup script. It accepts either the `.env`
block copied from the WorkOS application page:

```bash
scripts/setup/workos.sh --env-file /secure/path/workos.env
```

or a public Client ID plus an API-key file:

```bash
scripts/setup/workos.sh \
  --client-id client_01EXAMPLE \
  --api-key-file /secure/path/workos
```

It resolves the callback from Codefly, validates the WorkOS API key,
application JWKS, and exact registered callback, refuses to write files that
are not Git-ignored, installs both resolved files with mode `0600`, runs
`codefly doctor`, and never accepts the API key as a command-line argument.
See [`scripts/setup/README.md`](./scripts/setup/README.md) for the shared
external-provider setup contract.

For manual setup, create the operator-owned Codefly configuration files:

```bash
cp configurations/local-dogfood/identity.env.example \
  configurations/local-dogfood/identity.env
cp configurations/local-dogfood/identity.secret.env.example \
  configurations/local-dogfood/identity.secret.env
```

Edit `identity.env` with the staging Client ID and matching JWKS URL. Edit
`identity.secret.env` with the staging Client Secret/API key.
Both resolved files are ignored by Git.

For a team workspace, prefer a reference-only
`configurations/local-dogfood/identity.secret.ref.env` backed by the secret provider
declared on the `local-dogfood` environment. Do not create both the plaintext
and reference-only secret files for the same configuration.

Validate the dogfood profile without starting the graph:

```bash
codefly doctor workspace --env local-dogfood
```

## Run the product

Start with a genuinely new WorkOS user and no seeded product state:

```bash
codefly run service auth-sidecar --env local-dogfood
```

Start the same real provider with an optional state pack:

```bash
codefly run service auth-sidecar \
  --env local-dogfood \
  --fixture dev-admin
```

The optional fixture seeds background product state; it does not make its
fixture identities sign-in candidates under WorkOS. Use no fixture for the
clean first-user/onboarding experience. Use a fixture when you want the real
WorkOS user to dogfood against an already-populated installation.

For platform-admin dogfooding, set `BOOTSTRAP_ADMIN_EMAIL` in
`configurations/local-dogfood/application.env` to the real WorkOS email before
its first login. The bootstrap slot is one-use. When also using `dev-admin`,
choose an email not already present in that fixture; fixture identities and
external identities intentionally do not merge merely because their email
strings match.

Open the product ingress printed by:

```bash
codefly endpoint auth-sidecar --type rest --require-up
```

The hosted WorkOS ceremony is the only browser-owned authentication boundary.
After callback, onboarding executes through the headless TypeScript
application library; React only renders its state and forwards user intent.

## Generic identity configuration contract

Public values live in `identity.env`; secrets live in
`identity.secret.env` or `identity.secret.ref.env`.

| Key | Required | Meaning |
|---|---:|---|
| `IDENTITY_PROVIDER` | yes | `fixture`, `workos`, `auth0`, or `google` |
| `IDENTITY_DISPLAY_NAME` | browser providers | Sign-in button label |
| `IDENTITY_CLIENT_ID` | browser providers | Provider application Client ID |
| `IDENTITY_CLIENT_SECRET` | browser providers | Provider exchange credential; secret |
| `IDENTITY_AUTHORIZE_URL` | browser providers | Hosted authorization endpoint |
| `IDENTITY_TOKEN_URL` | browser providers | Authorization-code exchange endpoint |
| `IDENTITY_ISSUER` | optional | Signed-token issuer override |
| `IDENTITY_JWKS_URL` | optional | Signed-token JWKS override |
| `IDENTITY_AUTHORIZE_SELECTOR` | WorkOS | `authkit` for hosted AuthKit |
| `IDENTITY_ALLOWED_REDIRECT_URIS` | optional | Static fallback for trusted traffic that bypasses auth-sidecar |
| `IDENTITY_MANAGEMENT_API_KEY` | optional WorkOS adapter | WorkOS Admin Portal/SSO-management credential; secret |

The Next.js service agent exposes only non-secret `IDENTITY_*` values to the
browser. Accounts reads both public and secret values through the Codefly SDK.
No product service reads a local `.env` file or owns a second identity
configuration path.
