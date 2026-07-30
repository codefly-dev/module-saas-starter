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

## Next providers

Use the same contract for additional adapters as their Codefly configuration
boundaries are introduced:

| Script | Codefly capability | Provider resources |
|---|---|---|
| `stripe.sh` | `billing` | products, prices, webhook endpoint and signing secret |
| `resend.sh` | `email` | sending domain, sender identity and API key |
| `posthog.sh` | `product-analytics` | project, capture key, deletion credential and host |
| `sentry.sh` | `observability` | frontend/backend projects, DSNs and release token |

Do not add scripts that inject raw shell environment variables directly into a
service. Introduce the generic Codefly capability first, then automate its
provider adapter.
