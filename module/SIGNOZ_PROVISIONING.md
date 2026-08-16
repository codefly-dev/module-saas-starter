# SigNoz provisioning qualification

- Status: **unsupported**
- Qualified on: 2026-08-14
- Upstream candidate inspected: [SigNoz v0.137.1](https://github.com/SigNoz/signoz/releases/tag/v0.137.1)
- Provider repository: not created

The Starter does not currently provision SigNoz dashboards or alert rules. The
static API surface is promising, but there is no pinned `service-signoz`
deployment against which to prove the lifecycle. Creating a provider before
that boundary exists would turn an untested upstream API into a claimed Codefly
contract.

This decision does not affect application telemetry. Applications continue to
send OTLP to the in-graph collector endpoint resolved through Codefly; they do
not read or hard-code that address. `OTEL_EXPORTER_OTLP_ENDPOINT` remains only
the collector's external OTLP/HTTP destination. Dashboard provisioning is not
an application runtime capability.

## Qualification result

The candidate's generated
[OpenAPI document](https://github.com/SigNoz/signoz/blob/v0.137.1/docs/api/openapi.yml)
was inspected together with the tagged implementation. It is static evidence
only, not a substitute for exercising the pinned service.

| Boundary | Candidate evidence | Result |
|---|---|---|
| Version stability | The OpenAPI `info.version` is empty. The v0.137.0 release immediately before the candidate introduced dashboard authorization changes and substantially changed the dashboard handler and generated API document. | Not qualified without the immutable version selected by `service-signoz` and upgrade evidence. |
| Authentication | The document defines `SigNoz-Api-Key` and bearer-token authentication. Dashboard operations use fine-grained dashboard scopes; alert-rule operations use viewer/editor roles. | The shapes are usable, but service-account creation, least privilege, invalid credentials, and scope denial have not been exercised on the pinned deployment. |
| Complete observation | Dashboard listing exposes `limit`, `offset`, and `total`. Alert-rule listing returns one unpaginated array. | Dashboard traversal is representable. Complete rule observation, server limits, and concurrent-page behavior remain unverified. |
| Ownership metadata | Dashboard tags and alert labels round-trip in the documented request and response schemas. Both are mutable resource data. | A unique Codefly ownership stamp is representable, but uniqueness, filtering, duplicate detection, and hostile marker edits have not been proven. Provider state alone must never authorize mutation. |
| Create idempotency | Dashboard and rule creates return server-generated IDs and document no idempotency key. The tagged dashboard handler notes that the dashboard-name unique index does not yet exist. | An interrupted create cannot be retried safely until marker-based recovery is proven against real storage and list semantics. |
| Update and delete | Dashboards expose get, put, patch, and delete; rules expose get, put, patch, and delete. The dashboard API rejects writes to locked dashboards. Neither resource documents an ETag or another conditional-write precondition. | Drift is observable in principle, but update races, delete retries, and lost responses are not qualified. |
| Rate limiting | Dashboard and rule operations do not document `429` or `Retry-After` responses. | Backoff and retry behavior must be captured from the deployed boundary; it cannot be inferred from the schema. |
| Network boundary | The proposed service and its module-visible query endpoint do not exist yet. | A provider cannot resolve a governed semantic endpoint, and public ingress must not be introduced as a workaround. |
| Dogfood | There is no pinned local `service-signoz` deployment. | The required create/observe/plan/apply cycle and empty second plan cannot run. |

The upstream churn evidence is available in the
[v0.136.1 to v0.137.0 comparison](https://github.com/SigNoz/signoz/compare/v0.136.1...v0.137.0)
and the [v0.137.0 release notes](https://github.com/SigNoz/signoz/releases/tag/v0.137.0).

## Requalification gates

Provisioning can be reconsidered only after all of these are true:

1. The [capability ownership decision](https://github.com/codefly-dev/module-saas-starter/issues/126)
   is merged with SigNoz limited to APM traces, metrics, logs, dashboards, and
   alerts.
2. [`service-signoz`](https://github.com/codefly-dev/module-saas-starter/issues/129)
   is released with an immutable SigNoz version, a module-visible semantic
   query endpoint, and a host-held management credential.
3. Sanitized HTTP fixtures from that exact deployment prove authentication,
   authorization, dashboard pagination, complete rule listing, marker
   round-trips, drift, rate limiting, and connection loss before and after each
   mutation.
4. Conformance tests prove complete observations, deterministic plans, exact
   ownership/import semantics, duplicate-marker rejection, retain-by-default
   deletion, explicit destroy, and fail-closed handling of incomplete or
   ambiguous observations.
5. Local dogfood creates a dashboard and alert through `service-signoz`,
   observes them through the semantic endpoint, and produces an empty second
   plan.

If those gates pass, the provider may project only dashboard and alert
management. It must not project an OTLP endpoint, application runtime secret,
error tracking, product analytics, session replay, or feature flags. Only a
resource whose remote ID and exact ownership stamp match provider state, or one
adopted by an explicit import, may be updated or explicitly destroyed. Removing
configuration retains remote resources by default.
