# Local SaaS dogfooding

This profile runs the SaaS starter as an actual local product. It is not a
test suite: Codefly starts the product ingress, frontend, Accounts, PostgreSQL,
Vault, cache, object storage, durable jobs, and the integration adapter for the
configured external identity provider.

## Runtime composition

Two Codefly inputs are intentionally independent:

| Input | Authority | Purpose |
|---|---|---|
| `--env local-dogfood` | `configurations/local-dogfood/*` | Select independently configured identity, billing, email, analytics, error, and abuse adapters, and the telemetry destination |
| optional `--fixture <name>` | `module/fixtures/<name>.yaml` | Seed a useful starting product state |

A fixture never changes the identity provider. The ordinary `local`
configuration selects `IDENTITY_PROVIDER=fixture`; the `local-dogfood`
configuration selects `IDENTITY_PROVIDER=workos`. This allows a real WorkOS
login with either an empty database or an optional seeded product scenario.

The module services depend only on the generic Codefly workspace
configuration named `identity`. WorkOS is one adapter for that capability;
the onboarding application library does not import or select WorkOS.

Frontend is the public product endpoint. Its same-origin proxy stamps the
actual request origin with Codefly's internal service credential before
forwarding an API request to auth-gateway. Accounts uses that verified origin
for OAuth callbacks, WebAuthn, email links, and request-driven billing
redirects. The optional Codefly `application` origin remains only as a
production/background fallback; a local port never belongs in product config.

## Configure providers through Codefly

WorkOS is the identity adapter. The production-grade dogfood stack also
includes Stripe, Resend, PostHog, Sentry, the OpenTelemetry collector
destination, and Cloudflare Turnstile. Each one is a Codefly configuration group on this
profile: copy the committed `configurations/local-dogfood/<group>.env.example`
(and `<group>.secret.env.example`, where there is one) to the Git-ignored
`<group>.env` / `<group>.secret.env`, and fill in the values. Every group is
optional except `observability` and `email` — see the notes below. Stripe setup belongs to
the [`codefly-dev/provider-stripe`](https://github.com/codefly-dev/provider-stripe)
plugin, which validates the account, observes the webhook and projects the
`billing` group.

| Group | Provider |
| --- | --- |
| `identity` | WorkOS |
| `billing` | Stripe (through `codefly-dev/provider-stripe`) |
| `email` | Resend |
| `product-analytics` | PostHog |
| `error-tracking` | Sentry |
| `observability` | the OpenTelemetry collector the cell provides, or none |
| `abuse-protection` | Cloudflare Turnstile |

Validate the result with `codefly doctor workspace --env local-dogfood`.

`observability` is the one group this profile cannot skip. `accounts` and
`auth-gateway` declare it as a workspace-configuration-dependency, and on
`local-dogfood` the real `observability.env` is Git-ignored, so there is no
committed default to fall back on. Codefly skips a declared group whose files are
absent rather than failing, and both services refuse to start without an explicit
`TELEMETRY_STATE` — a group that did not arrive is not a cell without a collector,
and they will not guess. Copy `observability.env.example` as it is to run with
none: `TELEMETRY_STATE=absent` and its reason, traces on wool's stdout tracer,
metrics not exported. To export to a collector this machine can reach, set
`TELEMETRY_STATE=available` and `OTEL_EXPORTER_OTLP_ENDPOINT` as the example says.

`email` is required because this profile is not the `local` environment, and
accounts refuses to start there without an explicit `EMAIL_PROVIDER`: `resend`
with its secrets, or `disabled` to run without email — invitations then hand the
inviting administrator a link to share instead of sending one.

The product callback address always comes from
`codefly endpoint frontend --type http`. WorkOS and the browser can use its
loopback URL directly. Stripe CLI can forward to it. Resend and remotely
registered Stripe webhooks need a public HTTPS tunnel or deployed ingress,
while Codefly continues to own every internal host and port.

### WorkOS

See the product-ingress URL before starting it:

```bash
codefly endpoint frontend --type http
```

Append `/auth/callback` and register that exact URI in the WorkOS staging
application. WorkOS staging accepts localhost callback URIs. Do not copy this
port into the starter configuration: Codefly owns endpoint allocation.

Create the operator-owned Codefly configuration files:

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
codefly run service --env local-dogfood
```

Start the same real provider with an optional state pack:

```bash
codefly run service \
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
codefly endpoint frontend --type http --require-up
```

The hosted WorkOS ceremony is the only browser-owned authentication boundary.
After callback, onboarding executes through the headless TypeScript
application library; React only renders its state and forwards user intent.

## Generic identity configuration contract

Public values live in `identity.env`; secrets live in
`identity.secret.env` or `identity.secret.ref.env`.

| Key | Required | Meaning |
|---|---:|---|
| `IDENTITY_PROVIDER` | yes | `fixture`, `workos`, `auth0`, `google`, or `header-jwt` |
| `IDENTITY_DISPLAY_NAME` | browser providers | Sign-in button label |
| `IDENTITY_CLIENT_ID` | browser providers | Provider application Client ID |
| `IDENTITY_CLIENT_SECRET` | browser providers | Provider exchange credential; secret |
| `IDENTITY_AUTHORIZE_URL` | browser providers | Hosted authorization endpoint |
| `IDENTITY_TOKEN_URL` | browser providers | Authorization-code exchange endpoint |
| `IDENTITY_ISSUER` | optional | Signed-token issuer override (expected `iss`) |
| `IDENTITY_JWKS_URL` | optional | Signed-token JWKS override |
| `IDENTITY_AUTHORIZE_SELECTOR` | WorkOS | `authkit` for hosted AuthKit |
| `IDENTITY_ALLOWED_REDIRECT_URIS` | optional | Static fallback for trusted traffic that bypasses auth-gateway |
| `IDENTITY_MANAGEMENT_API_KEY` | optional WorkOS adapter | WorkOS Admin Portal/SSO-management credential; secret |

### `header-jwt` provider

For deployments behind a customer-operated access gateway (PingAccess, an
nginx/Apache auth module, some API gateways) that authenticates the user
upstream and injects a signed JWT in a request header. There is no OAuth
ceremony: the login route reads the configured header, verifies the JWT, and
mints our own session.

| Key | Required | Meaning |
|---|---:|---|
| `IDENTITY_HEADER_NAME` | yes | Request header the login route reads the JWT from. Consumed at `/auth/login` only; never forwarded downstream. |
| `IDENTITY_JWKS_URL` | yes¹ | JWKS used to verify the header JWT signature |
| `IDENTITY_AUDIENCE` | yes | Expected `aud`; always enforced, including under perimeter-trust decode |
| `IDENTITY_ISSUER` | optional | Expected `iss` when set |
| `IDENTITY_PROVIDER_NAME` | optional | `user_identities.provider` key; defaults to `header-jwt` |
| `IDENTITY_SUBJECT_CLAIM` | optional | Subject claim; defaults to `sub` |
| `IDENTITY_EMAIL_CLAIM` | optional | Email claim; defaults to `email` |
| `IDENTITY_EMAIL_VERIFIED_CLAIM` | optional | Email-verified claim; defaults to `email_verified` |
| `IDENTITY_NAME_CLAIMS` | optional | Comma-separated claims joined into the display name (e.g. `given_name,family_name`) |
| `IDENTITY_GROUP_CLAIM` | optional | Group claim (string or array); enables the group gate |
| `IDENTITY_ALLOWED_GROUPS` | optional | Comma-separated allow-list; login is denied with a distinct "access not granted" when the group claim does not overlap. Unset skips the gate. |
| `IDENTITY_PERIMETER_TRUST_DECODE` | optional | `true` decodes the header JWT without signature verification (still enforcing `exp`/`aud`). Off by default. Only safe when the gateway is the sole ingress and strips client-supplied copies of the header. |

¹ Required unless `IDENTITY_PERIMETER_TRUST_DECODE=true`, which is the only mode
that omits signature verification. Every other configuration fails closed: an
unreachable JWKS denies the login rather than decoding without verification.

The Next.js service agent exposes only non-secret `IDENTITY_*` values to the
browser. Accounts reads both public and secret values through the Codefly SDK.
No product service reads a local `.env` file or owns a second identity
configuration path.
