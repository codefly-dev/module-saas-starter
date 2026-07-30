# Dogfooding the saas-starter

> A scripted walk-through of every shipped feature, organized by what
> you need wired (nothing → fixture → real Stripe → real identity).
> Tick boxes as you go; report regressions inline as commits or
> issues.

For a runnable local product with a real external identity provider, begin
with [LOCAL_DOGFOODING.md](./LOCAL_DOGFOODING.md). This checklist then
exercises capabilities inside that Codefly-managed dogfood graph.

---

## Pre-flight

Run **once** to make sure the agents and module are aligned with the
current source.

```bash
# 1. Build agents (s3 plugin v0.0.2 must be installed locally)
cd ~/Development/deus/codefly.dev/agents/services/s3 && codefly agent build

# 2. Enter the standalone starter repository
cd ~/Development/deus/codefly/module-saas-starter

# 3. Boot the product ingress and its complete dependency graph
codefly run service --fixture dev-admin
```

Expected graph: `vault + store + cache + object-storage (s3 plugin
→ MinIO at port 9xxx) → accounts → frontend → auth-sidecar`. Independent
services may start concurrently. If any service
hangs at "waiting for ready", check that step's `--debug` output.

The TUI shows green dots when each service is up. Wait for **frontend** to
report its public HTTP URL. Open that URL; its same-origin API proxy routes
backend traffic through auth-sidecar.

---

## Tier 1 — fixture-only (no external creds needed)

These exercise everything that doesn't need Stripe / WorkOS / a real
SMTP. Every box should pass cleanly with just the dev-admin fixture.

### Login + identity

- [ ] `/auth/login` shows the four fixture users (Sarah / Alice / Bob / Carol).
- [ ] Click **Sarah Chen** (super_admin). Lands on `/`. "Welcome back" hero visible.
- [ ] Sidebar shows admin groups: Users & Access · Platform · Billing · Integrations · Developer.
- [ ] Open `/admin` — every nav link returns 200, no 403/404.
- [ ] Logout (avatar dropdown). Lands on `/auth/login`. Browser cookie cleared.

### Theme toggle (new)

- [ ] Header has a sun/moon/monitor icon next to the bell.
- [ ] Click it — three options: Light · Dark · System. Selected gets a check.
- [ ] Pick **Dark**. Page repaints immediately. Reload — still dark.
- [ ] Pick **System**. macOS dark-mode toggle now drives the page.

### General settings (new)

- [ ] Visit `/settings`. Three cards: Appearance · Time & date · Email preferences.
- [ ] Change theme dropdown to **Light** + click "Save appearance" — toast "Settings saved".
- [ ] Reload — theme is light.
- [ ] **Open in another browser** (or incognito) → log in as Sarah → /settings reflects light. ThemeSync working cross-device.
- [ ] Set timezone to `Europe/Paris`, time format `24h` — Save. Reload — preserved.
- [ ] Toggle off Marketing email — Save. Reload — toggle stays off. Security alerts checkbox is **disabled-checked** (forced on).

### Org admin chrome

- [ ] `/admin/users` — 4 fixture users, table renders, can sort by columns.
- [ ] `/admin/organizations` — 1+ orgs (Acme Corp).
- [ ] `/admin/organizations/settings` — change logo URL → save → toast.
- [ ] `/admin/teams` — empty or seeded team.
- [ ] `/admin/roles` — system + custom roles list.
- [ ] `/admin/sessions` — current session shown.
- [ ] `/admin/audit-log` — events from the seeding flow + your logins.

### API keys + scopes (new picker)

- [ ] `/admin/api-keys` — pick **Acme Corp** in the org-selector → empty table or seeded keys.
- [ ] Click **Create Key** — dialog opens.
- [ ] Form has Name + Environment (Live/Test) + **Scopes** section (NEW).
- [ ] Scopes radio offers: Read-only · Read & write · Webhook management · No scopes.
- [ ] Pick "Read-only", name it `dogfood-readonly`, Live, Save.
- [ ] Plaintext key shown once. **"Copy the key first"** button is disabled until you click Copy.
- [ ] Click Copy → "Done" enables → click Done. Key visible in table with `cfly_sk_…` prefix mask.
- [ ] Revoke that key — confirmation dialog → Revoke → row tagged "Revoked", sinks to bottom of list.

### Webhooks v2

- [ ] `/admin/webhooks` — pick Acme Corp.
- [ ] Click **Create Webhook** — dialog. Fill `https://example.com/dogfood` + tick `User Created` → Create.
- [ ] Toast "Webhook created". Row appears.
- [ ] Row actions menu (⋮) → **Test**. Toast "Test delivery sent".
- [ ] Click the row — Deliveries panel opens (right rail). At least 1 row showing 404 or connection-refused.
- [ ] Click **Replay** in the detail pane. Toast "Replayed delivery". List grows by one.
- [ ] Row actions menu → **Rotate secret**. window.confirm → OK → New-Secret dialog.
- [ ] Dialog shows the new `whsec_…`. **"I've saved it" button is disabled until you Copy.** Click Copy → enable → I've saved it → dialog closes.
- [ ] Delete the webhook. Toast.

### Audit-export (new admin form)

- [ ] `/admin/audit-export` — pick Acme Corp. EmptyState gone, form visible.
- [ ] Fill bucket=`dogfood`, accessKeyId=`minioadmin`, secret=`minioadmin`, endpoint=`http://localhost:<minio-port>` (read the port from the codefly run TUI; it's the object-storage TCP endpoint).
- [ ] **Save** with the right port. Toast "Audit export saved". Form flips to Update mode. Hint "(preserved)" appears next to secret input.
- [ ] **Save with wrong port** (e.g. `http://localhost:1`). Toast "Save failed: connection probe failed: ...". Row NOT persisted. Page stays in first-config mode.
- [ ] Save with the right port again. After ~60s an export tick should fire. Audit-log surface shows `audit_export.configured`. ExporterStatus banner flips to green "Last exported …".
- [ ] **Verify in MinIO**: `mc alias set local http://localhost:<port> minioadmin minioadmin && mc ls --recursive local/dogfood` — see `<yyyy-mm-dd>/<unix-ms>.jsonl` objects with audit events.
- [ ] Click **Remove configuration**. Confirm. Form returns to first-config state.

### SSO admin — stub mode (new)

- [ ] Ensure `IDENTITY_MANAGEMENT_API_KEY` is absent from the selected
      Codefly `identity` configuration.
- [ ] `/admin/sso` — pick Acme Corp. Status = "Not configured", "Set up SSO" CTA visible.
- [ ] Click "Set up SSO". Same-tab redirect to `/admin/sso?demo=1`.
- [ ] After redirect, status = "Setup pending". "Continue setup" + "Disable SSO" buttons.
- [ ] Click **Disable SSO** → confirm → toast "SSO disabled". Status = "Disabled". "Re-enable SSO" button.
- [ ] Click "Re-enable SSO". Goes through the same flow. Status returns to "Setup pending".
- [ ] Audit log shows `sso.setup.started` + `sso.disabled` events.

### Billing admin — no-Stripe-key callout (new)

- [ ] Ensure STRIPE_API_KEY is **unset**.
- [ ] `/admin/billing` — pick Acme Corp.
- [ ] Plan card shows "Free" badge + "X features included" + Manage subscription button.
- [ ] Click **Manage subscription** → toast "Couldn't open portal: billing not configured". (Expected.)
- [ ] Usage card shows top-3 by % used (likely just `seats` since others are 0-used).
- [ ] Invoices card shows the friendly "Stripe not configured" callout pointing at the `STRIPE_API_KEY` codefly secret.

### Entitlements

- [ ] `/admin/entitlements` (super_admin only) — pick Acme. Free plan rows: seats, api_keys, api_calls_monthly, sso, audit_log.
- [ ] Click **Override** on `seats` → set to `999` + reason "dogfood test" → Apply. Row updates, **Override** badge appears.
- [ ] Verify `/admin/billing` usage card now shows seats out of 999.

### Rate-limit visibility (new)

- [ ] Open browser devtools Network tab on any /admin page. Refresh.
- [ ] On any RPC call, response headers include `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`.
- [ ] (Hard to trigger the banner without 900+ requests in 60s — skip unless you have a load script handy. Or temporarily lower `defaultLimit` to 5 in `pkg/adapters/rate_limit_interceptor.go` and refresh a few times to see the bottom-center amber pill.)

### MFA

- [ ] `/settings/mfa` — TOTP setup dialog. Scan QR with Authenticator app, enter code → "MFA enabled".
- [ ] Logout, log back in → access granted (cookie still trusted from this device for now).
- [ ] Try a sensitive op that requires MFA (e.g. webhook RotateSecret) — should now succeed because mfa_satisfied=true on the JWT.

### GDPR + consent

- [ ] `/settings/data` — export request → kicks off export. Should land in a downloadable URL (or queue notification).
- [ ] Visit any page in incognito (logged out) → ConsentBanner appears bottom-right. Click Accept → banner gone, localStorage key set.
- [ ] Log in (any user) → after a moment ConsentBanner reappears (because terms version on server > stored). Click Accept → server records acceptance. Reload → banner stays gone. Audit log has `consent.accepted`.

### Status page

- [ ] `/status` (publicly reachable, no auth) — JSON probe table: postgres, redis, vault green dots. Latency in ms.

### Command palette

- [ ] Cmd+K (or Ctrl+K) opens the palette.
- [ ] Type `web` → "Webhooks" appears, click → routes to /admin/webhooks.
- [ ] Type a fixture user email → matches via super_admin search.
- [ ] Esc closes.

---

## Tier 2 — Stripe test mode

Configure the generic Codefly `billing` capability. The script refuses live
keys and resolves the local signed-webhook callback from the product ingress:

```bash
# Terminal A: keep this running and save the displayed signing secret.
stripe listen --forward-to \
  "$(codefly endpoint frontend --type http)/v1/billing/webhook"

# Terminal B: configure the secret from that same listener, then run the stack.
scripts/setup/stripe.sh \
  --api-key-file /secure/path/stripe.env \
  --webhook-secret-file /secure/path/stripe-webhook.env
codefly run service --env local-dogfood --fixture dev-admin
```

Use the signing secret printed by the same Stripe CLI listener. For a remotely
registered webhook, expose the ingress through public HTTPS and use
`--webhook-origin ... --provision-webhook`; the script rejects remote
provisioning to localhost.

- [ ] `/admin/billing` — Plan card shows **Pro** badge.
- [ ] Click **Manage subscription** — redirects to Stripe-hosted billing portal.
- [ ] Update card / cancel subscription → return → status reflects.
- [ ] **Invoices card** — shows the test invoice. Click invoice number → opens hosted detail. Click PDF icon → downloads.

---

## Tier 3 — real WorkOS identity and SSO management

Configure WorkOS exclusively through the `local-dogfood` Codefly profile:

```bash
codefly doctor workspace --env local-dogfood
codefly run service --env local-dogfood
```

See [LOCAL_DOGFOODING.md](./LOCAL_DOGFOODING.md) for the generic public and
secret identity manifests. `IDENTITY_MANAGEMENT_API_KEY` enables the WorkOS
Admin Portal integration; `IDENTITY_CLIENT_ID` and
`IDENTITY_CLIENT_SECRET` enable actual hosted login.

- [ ] `/admin/sso` — pick Acme Corp. Click "Set up SSO".
- [ ] Browser redirects to `https://api.workos.com/portal/...` admin portal.
- [ ] Configure a connection (SAML or OIDC) using your test IdP.
- [ ] After the portal flow, redirect back to `/admin/sso`. Status = "Active". connection_id populated.
- [ ] (Optional, big spend) Configure your IdP to allow a test user with email matching Acme's domain. Log out, attempt login with that email — auth-sidecar should route to WorkOS via the connection_id.

---

## Tier 4 — full provider telemetry and abuse stack

Configure the remaining adapters using
[`scripts/setup/README.md`](./scripts/setup/README.md), then start the same
Codefly-managed graph:

```bash
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
codefly run service --env local-dogfood
```

- [ ] Create and resend an invitation. Resend shows one provider message and the admin invitation changes `queued → sent → delivered`.
- [ ] Replay a Resend webhook. The API returns `200`, but the `svix-id` ledger contains only one event and state does not regress.
- [ ] Tamper with a forwarded Resend payload or use an old timestamp. The receiver returns `400` and writes nothing.
- [ ] Grant analytics consent, navigate through onboarding, and verify bounded browser plus durable backend events in PostHog.
- [ ] Withdraw analytics consent or log out. Browser capture stops immediately and identity resets.
- [ ] Trigger controlled browser and backend errors. Sentry correlates release, environment, and W3C trace context.
- [ ] With `otel.sh --debug`, exercise login/onboarding and observe trace/metric/log summaries from the in-graph telemetry service.
- [ ] Switch Turnstile to `--fixture fail --force`. Registration and waitlist submission fail without database writes.
- [ ] Switch Turnstile to `--fixture replay --force`. The first deterministic verification follows Cloudflare's fixture behavior; replay rejection leaves state unchanged.

---

## Test status

| Surface | Test layer | Count |
|---|---|---|
| Backend (Go) | unit + integration | 30 files · 206 funcs across 12 packages |
| Backend (key new code) | unit | s3 plugin (6) · audit-exporter resolveEndpoint (5 cases) · sso_admin stub-mode (3) · user_settings (3) · scope (5) |
| FE (Vitest) | unit | 26 files · 210 tests |
| FE (Playwright) | e2e | 9 specs · 39 tests (login, navigation, admin-flow, auth-boundary, command-palette, revocation, webhooks, audit-export, sso-admin) |

Run them:

```bash
# Backend — Codefly asks the service agent to run the Go suites.
cd module/services/accounts
codefly test service

# FE unit — Codefly asks the service agent to run Vitest.
cd ../frontend
codefly test service

# FE e2e (heavy — Codefly owns the dependency graph)
codefly test service --suite e2e
```

---

## Known gaps you'll hit during dogfood

- **API key auth direct-to-api in dev**: scope enforcement is wired
  on the handlers but the Connect interceptor in dev-mode (no auth
  sidecar) doesn't validate `cfly_sk_*` keys. Dogfood with JWT auth.
- **Email-send flows**: magic links / verification emails / transactional
  notifications log only in dev. Real Resend integration is wired but
  not exercised in fixture mode.
- **Per-org subdomains**: not yet wired. Acme is reached at the same
  origin as everyone else.
- **i18n**: locale picker stores the preference, but UI text is
  English-only — no translation files yet.
- **API-key Revoke gating**: still requires platform_admin per a
  TODO in `pkg/adapters/rpcs.go`. Org admins can't revoke their org's
  own keys today; they can create them. Fix is in the backlog.

If anything in Tier 1 is broken, **stop and report** — that's the
fixture-only happy path that should never need creds.
