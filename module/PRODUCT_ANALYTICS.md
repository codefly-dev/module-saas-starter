# Product analytics and business metrics

This module keeps four telemetry planes separate:

| Plane | Purpose | Source of truth |
| --- | --- | --- |
| Security audit | Protected mutations and actor/resource history | `audit_events` |
| Billing usage | Quota, invoice, and reconciliation correctness | `usage_events` and `usage_totals` |
| Operational telemetry | Availability, latency, errors, queues, and integrations | OpenTelemetry, Sentry, and job projections |
| Product analytics | Behavior, activation, retention, and growth | Canonical product events and the configured analytics sink |

Product events must not be inferred from audit rows or usage receipts. A domain
outcome may legitimately produce one record in more than one plane, with a
different contract and retention policy in each.

Which tool owns each observability, feature-flag, and analytics capability —
and why PostHog owns analytics but not flags, errors, or APM traces — is fixed
in `docs/adr/0002-observability-flags-analytics-capability-ownership.md`.

## Canonical event contract

`services/accounts/proto/saas/analytics/v1/events.proto` owns the generated,
vendor-neutral envelope. Every event contains a UUID event identity, stable
lower-snake-case name, schema version, producer and ingestion timestamps,
nullable user/anonymous/organization/session identities, source, bounded
context, registered properties, and resolved privacy policy.

`services/accounts/code/pkg/analytics/registry.json` is the event registry. It
contains all starter acquisition, identity, organization, invitation,
onboarding, value, billing, customer-voice, support, and notification facts.
Every entry declares its owner, description, allowed sources, purpose, allowed
properties and their scalar types, PII class, retention, and schema version.
The registry parser and contract tests fail on unknown fields, duplicate
names, forbidden properties, unused type overrides, or incomplete metadata.

Names describe completed facts, such as `invite_accepted`, rather than
components or clicks. Backend outcomes use their domain operation identity;
optional views and intents are browser-authored only after the corresponding
purpose has resolved to granted.

Ordinary properties are limited to 32 registered keys and 32 KiB after
encoding. Strings, lists, and nesting are bounded. Email addresses, credential
shapes, and names containing token, secret, authorization, cookie, message,
body, content, password, email, or phone vocabulary are rejected. Payloads do
not contain access tokens, invitations, provider secrets, message bodies, or
unrestricted user text. Context values receive the same sensitive-value checks;
routes must be templates or paths without query strings or fragments.

### Evolution

- Adding an optional registered property without changing existing meaning is
  compatible within the current schema version.
- A type, meaning, identity, purpose, source, or required-property change is
  breaking and requires a new positive schema version.
- Renaming a fact creates a new event. The old name remains registered through
  its retention window and is marked deprecated before producers are removed.
- Historical events are interpreted with the definition version captured on
  the event; definitions are never retroactively rewritten.
- Registry changes require the Go registry, validation, idempotency, and
  adapter tests plus the browser allowlist when web is an allowed source.

`analytics.CheckCompatible` is the CI contract guard: an event cannot
disappear, lose or retype a property, or change its purpose, privacy class, or
allowed sources at the same schema version. Additive properties and new events
remain compatible.

## Durable backend export

Backend domain services depend on `analytics.Emitter`, not a vendor package.
`analytics.Outbox` validates and deterministically encodes an event into the
generic `analytics` job queue. Its event UUID is the producer idempotency key.
The request-scoped Postgres producer requires the surrounding user or
organization transaction, so the domain mutation and event command commit or
roll back together.

The leased worker revalidates the immutable envelope, tenant scope, event
identity, privacy constraints, and payload bounds before delivery. Registry
validation happens before enqueue so a compatible deployment cannot reject an
older, already-durable schema after rollout. Retries preserve the event UUID;
PostHog receives it as both its logical UUID and an ordinary `event_id`.
Provider failure cannot extend or fail the original domain transaction.
Successful provider references are recorded in `analytics_deliveries`.

Modes are disabled by default:

| `PRODUCT_ANALYTICS_MODE` | Behavior |
| --- | --- |
| empty or `disabled` | No event outbox and no analytics network traffic |
| `noop` | Durable, validated delivery to a no-network sink |
| `posthog` | Durable server delivery through the PostHog adapter |

PostHog mode requires `POSTHOG_PROJECT_API_KEY`, `POSTHOG_PERSONAL_API_KEY`,
`POSTHOG_PROJECT_ID`, an absolute capture `POSTHOG_HOST`, and a separate
absolute management `POSTHOG_API_HOST`. The personal key needs the
person-deletion permission and is used only by durable suppression commands.
Non-local hosts must use HTTPS. The adapter has a five-second default request
timeout and a maximum batch of 100. Application and domain packages do not
import a PostHog SDK.

The in-memory sink is deterministic and rejects conflicting reuse of an event
UUID. It is the reference sink for contract and journey tests.

## Browser identity, attribution, and consent

`frontend/code/src/lib/analytics/browser.ts` provides the starter browser
interface. It only exposes the web subset of the registry. Collection,
identify, alias, and organization grouping are gated by the resolved purpose.
Denied or withdrawn consent stops collection immediately and resets the sink.

The anonymous UUID is stored locally and never placed in a URL. The first
successful identify aliases that UUID once. A different login on the same
device rotates the anonymous UUID before aliasing, and logout/reset rotates it
again. This avoids joining separate people who share a browser.

`attribution.ts` records first and last touch independently. It accepts bounded
UTM source/campaign values and stores only the referrer hostname, never a full
referrer URL. Route, release, environment, locale, session, feature-flag
variants, and attribution remain bounded context fields.

`posthog-browser.ts` is the optional browser reference adapter. It enforces
HTTPS outside local development and a bounded five-second default timeout.
Construct it only after product or marketing consent is granted. Replay has its
own purpose and remains off until the product supplies a consent and redaction policy.
The companion consent feature owns the UI and durable preference API; this
module consumes its resolved policy.

User or organization suppression is represented by the server adapter
interface. PostHog deletion uses the separately scoped personal key and the
management API origin; capture credentials are never treated as deletion
authority. Review both regional origins and the personal-key scope before
enabling PostHog for personal data.

## Activation and North Star

The starter's reference North Star is Weekly Activated Organizations: distinct
organizations completing the configured core-value event during a calendar
week. `pkg/metrics.WeeklyActivated` applies the effective versioned activation
definition at event time, deduplicates retries by event UUID, and produces both
organization and user counts in the chosen reporting timezone.

Checklist completion is not activation. A generated product adds its core
event to the registry and supplies an activation definition with:

1. a new immutable positive version;
2. an effective timestamp;
3. the registered completed-fact event name;
4. fixture events immediately before and after the effective boundary.

Changing the definition creates a new version. Dashboards segment the boundary
instead of combining unlike historical definitions.

## Recurring revenue

`pkg/metrics.MRRWaterfall` consumes one normalized organization/month snapshot
per currency through an explicit inclusive end month. Once an organization
appears, every month through that boundary requires a row; a transition to
zero requires an explicit voluntary or involuntary cause. It classifies new,
expansion, contraction, churned, and reactivated MRR, then calculates closing
MRR, ARR, paying organizations, ARPA, logo movements, GRR, and NRR. Exact
synthetic fixtures pin the expected amounts.

The input MRR amount is recurring subscription value after recurring discounts
and before tax. It excludes refunds, credits, one-time charges, usage
adjustments, and non-recurring invoice lines. Pauses and grace periods follow
the provider-normalized active-state policy. Proration changes the next
normalized recurring snapshot, not MRR by the temporary invoice amount.
Failed payments become involuntary churn only after the configured grace and
dunning policy ends. Corrections replace the affected source snapshot and
recompute subsequent movements.

Currencies cannot be summed. Deployments either publish one dashboard per
currency or convert snapshots using an explicit, versioned rate source before
calling the metric. Deleted or merged organizations retain a stable analytics
organization identity so history is not double counted. New business is
excluded from GRR and NRR; expansion and reactivation are included only in
NRR.

## Dashboards and truthful states

`pkg/metrics/dashboard_pack.json` is the versioned provider-neutral seed pack
for founder, acquisition, onboarding, product adoption, retention/churn,
revenue, usage/entitlement, and reliability/data-quality views. Every card has
a definition, source, executable SQL query, UTC reporting timezone, refresh
expectation, and owner. `go run ./cmd/measurement-pack` emits the dashboards,
PromQL alerts, and the generic warehouse materialization schema as one
deployable JSON bundle. Behavioral exploration maps to PostHog; curated
cross-domain values populate `measurement.metric_values`.

Frontend metric cards use the shared states `loading`, `no_data`, `partial`,
`stale`, `provider_unavailable`, `not_configured`, `sample`, and `ready`.
Sample mode is rejected in production. A source policy test rejects literal
numeric chart series in production TS/TSX. The account dashboard no longer
presents a hard-coded audit series, and the activity feed always binds to the
active organization.

## Operational metrics, alerts, and recovery

Job workers emit OpenTelemetry `saas.jobs.polls`, `claimed`, `active`,
`completed`, and `duration` instruments. Labels are limited to queue plus
bounded result/outcome vocabulary; user, organization, payload, event, and
provider identifiers are not metric labels. A production monitor exports
database-derived `saas.jobs.depth` and `saas.jobs.oldest_ready_age` gauges.
Analytics export adds delivery, duplicate, rejection, and provider-latency
instruments, while worker outcomes supply retry and terminal-failure counts.

`pkg/metrics/slo_pack.json` defines 30-day availability and p95 latency SLOs
for signup, login, invitation acceptance, checkout, the core action,
notification delivery, analytics export, and usage consumption. It also seeds
executable PromQL for multi-window burn, dead-letter, queue-age, integration,
export-health, and usage-reconciliation alerts. Diagnosis and recovery
procedures are in `MEASUREMENT_RUNBOOKS.md`.

## Retention, volume, and deployment

The default registry retention is 400 days, enough for year-over-year cohorts.
A deployment may shorten it by destination and region but must not silently
extend it. Keep event names and property keys low-cardinality; user,
organization, session, and event identities belong in fields, never metric
labels. Do not send repeated timers, cursor movement, full URLs, payload
contents, or autocapture by default.

For local and deterministic tests, use `disabled`, `noop`, or the memory sink.
For a hosted deployment, set the regional PostHog capture and management hosts
explicitly and keep the project key in server or consent-gated browser
configuration. A
privacy-oriented deployment can retain the no-op sink, point the same interface
at a regional warehouse adapter, and keep browser capture disabled.
