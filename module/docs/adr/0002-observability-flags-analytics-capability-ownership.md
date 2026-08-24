# ADR 0002: Observability, feature-flag, and analytics capability ownership

- Status: Accepted
- Date: 2026-08-24
- Issue: codefly-dev/module-saas-starter#98

## Context

The platform is adding its remaining observability, feature-flag, and product
analytics capabilities on top of the provider-versus-service split established
by the Sentry, Stripe, and Resend work (#71 / #72 / #73):

- A third-party tool that **runs in-cluster** is packaged as a **codefly
  service agent** that emits a Kubernetes manifest bundle; GitOps/ArgoCD
  deploys it, exactly like `store`, `cache`, and `temporal`.
- A **swappable business capability** is a **provider** behind a versioned
  contract, bound through a `workspace_configuration_dependencies` seam like
  `provider-sentry`, `stripe`, and `resend`.

The capabilities themselves already exist as named seams in the topology
bindings (`deployment/topology.bindings.codefly.yaml`): the frontend depends on
`error-tracking` and `product-analytics`, the `telemetry` collector depends on
`observability`, and `accounts` depends on `product-analytics`. A
`feature-flags` seam does not exist yet.

Several candidate tools span more than one capability. PostHog ships product
analytics, session replay, feature flags, and error tracking. SignOz covers
traces, metrics, and logs but also surfaces exceptions. Sentry does error
tracking and some performance tracing. Without a single owner per capability the
platform would run two or three tools for the same job, split each capability's
data across sinks, and leave the provider seam ambiguous for downstream provider
repos. #98 makes resolving ownership a precondition for building any of the
providers.

## Decision

### One owner per capability

Each capability has exactly one owning tool. A tool may technically offer a
neighbouring capability; that neighbour is out of scope for the tool and is not
wired, so its data has a single home and its provider seam has a single binding.

| Capability seam | Owning tool | Packaging | Not owned by |
| --- | --- | --- | --- |
| `observability` (APM traces, metrics, logs) | SignOz | `service-signoz` (+ optional `provider-signoz`) | PostHog, Sentry |
| `error-tracking` | Sentry | `provider-sentry` (SaaS) | PostHog, SignOz |
| `feature-flags` | Unleash | `service-unleash` (+ `provider-unleash`) | PostHog |
| `product-analytics` (incl. session replay) | PostHog | `provider-posthog` (SaaS) | SignOz |

Session replay is a purpose inside `product-analytics`, owned by PostHog. It is
not a separate capability seam and not a separate tool. It stays off until the
product supplies a consent and redaction policy, consistent with
`PRODUCT_ANALYTICS.md`.

### observability — SignOz (service)

SignOz runs in-cluster and is packaged as `service-signoz`, a ClickHouse-backed
OTLP collector plus query and UI. It is the destination for the existing
in-graph `telemetry` OTLP gateway, whose collector already "forwards trace,
metric, and log protobufs to a configured OTLP/HTTP destination". Applications
keep emitting OTLP through the SDK-resolved `telemetry` endpoint and, where they
export directly, via `OTEL_EXPORTER_OTLP_ENDPOINT`; OTLP export is
instrumentation configuration, not a per-service contract, so it introduces no
new provider seam.

`service-signoz` is **blocked on `service-clickhouse` publishing** — its agent
is currently unresolvable. Until ClickHouse is available, the `observability`
seam keeps its current behaviour: the collector logs locally or forwards to an
operator-configured OTLP/HTTP endpoint.

An optional `provider-signoz` provisions dashboards and alerts, mirroring
`provider-sentry`. It binds the `observability` seam for provisioning only; it
is not on the telemetry data path.

### error-tracking — Sentry (provider)

Error tracking remains owned by Sentry through `provider-sentry` behind the
`error-tracking@1` contract from #72. SignOz exception views and PostHog error
tracking are not wired. Keeping errors in one sink preserves a single
grouping/alerting surface and avoids duplicate provider bindings on the same
seam.

### feature-flags — Unleash (service + provider)

Feature flags are owned by Unleash behind a new `feature-flags@1` contract:

- `service-unleash` packages the Unleash server and its own PostgreSQL, with
  the admin API/UI at `module` visibility.
- **Unleash Edge** is exposed as a `public` endpoint so client SDKs evaluate
  flags at the load-balancer edge rather than against the admin server.
- `provider-unleash` implements `feature-flags@1` for server-side evaluation and
  flag administration.

The starter's existing DB-backed per-org flags (`accounts` `FeatureChecker`,
`useFeatureFlag()` on the frontend) remain the default, zero-dependency
implementation behind the same contract. A deployment that binds
`provider-unleash` swaps the evaluation source without changing call sites,
exactly as `PRODUCT_ANALYTICS_MODE` swaps analytics sinks. PostHog feature flags
are not wired.

### product-analytics — PostHog (provider, SaaS exception)

PostHog is the deliberate SaaS exception: self-hosting it pulls in ClickHouse
and Kafka, which is too heavy for the in-cluster service model. It stays a
hosted SaaS bound through `provider-posthog` behind `product-analytics@1`, which
formalises the PostHog adapter already described in `PRODUCT_ANALYTICS.md`
(`PRODUCT_ANALYTICS_MODE`, the durable server outbox, and the consent-gated
browser adapter). Egress is public; an ingest reverse-proxy is added only if
PostHog is ever self-hosted behind the same contract. PostHog does not own
flags, errors, or APM traces.

## Consequences

- Downstream provider and service repos (`service-signoz`, `provider-signoz`,
  `service-unleash`, `provider-unleash`, `provider-posthog`) can start against
  an unambiguous per-capability contract and seam. Each is its own repo, as with
  `provider-sentry` / `stripe` / `resend`.
- `service-signoz` cannot ship until `service-clickhouse` publishes; the
  `observability` seam's current local/forwarding behaviour is unchanged in the
  interim.
- A clean starter still runs with no external observability, flag, or analytics
  provider: OTLP logs locally, flags resolve against the DB implementation, and
  analytics stay on the disabled or no-op sink.
- Adding the `feature-flags` seam to the topology bindings and defining
  `feature-flags@1` is follow-up work tracked under #98; this ADR fixes the
  ownership those changes must follow.
- Choosing a neighbouring capability from an already-adopted tool later (for
  example PostHog flags) is a contract-ownership change that supersedes this
  ADR, not an ad-hoc second binding on the seam.
