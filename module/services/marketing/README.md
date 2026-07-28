# Marketing service

`marketing` is the separately deployable public company site. It does not share
an application process, authentication session, database, vault, object store,
admin client, or product feature package with `frontend`.

## Runtime contract

| Variable | Required | Purpose |
|---|---:|---|
| `MARKETING_ENABLED` | No | `false` serves an explicit disabled state; default is `true`. |
| `MARKETING_INDEXABLE` | Production | Enables canonical-host indexing. Staging and the fixture default to `noindex`. |
| `MARKETING_CATALOG_URL` | When pricing is public | Public origin for `GET /v1/public/plans`; credentials and cookies are never sent. |
| `MARKETING_RELEASE` | No | Public-service release identifier used by health and telemetry. |
| `MARKETING_STRICT_READINESS` | Production build | `1` rejects fixtures, placeholder domains/contacts, disabled indexing, and missing catalog configuration. |

These values are public locations, switches, and identifiers. This service has
no secret environment contract. `src/config/environment.ts` rejects credentials
embedded in URLs, while the configuration generator rejects secret-shaped
fields before they can enter browser or static output.

The catalog origin is an HTTPS public boundary, not an in-cluster Codefly
dependency. Local development may use localhost. This prevents dependency sync
from copying product protobuf/session contracts into the public service and
keeps extracted deployments independent of cluster DNS.

## Rendering and cache policy

- Repository content and route metadata are revalidated every five minutes.
- Fingerprinted Next.js assets use the framework's immutable cache policy.
- The feed uses a five-minute public cache with stale-while-revalidate.
- Health and readiness responses are never cached.
- Pricing never falls back to copied content. An unavailable catalog produces a
  degraded CTA state without affecting other pages or readiness.
- Draft and scheduled content is excluded from route inventory, feed, sitemap,
  search, and navigation. A due scheduled item fails the content gate until the
  publication job changes it to `published`.

## Local domains

With the local Istio gateway:

- `saas.localhost` and `www.saas.localhost` serve marketing;
- `docs.saas.localhost` serves the same marketing deployable;
- `app.saas.localhost` serves the authenticated product;
- legacy `localhost` remains a product fallback.

`status` is always an external incident system.

## Deploy, roll back, or disable

Marketing has its own Codefly service manifest, container, health route,
NetworkPolicy, and local/AWS ArgoCD Application. Deploy or roll it back by
syncing only the marketing Application to the intended image revision. Its
rollback has no database migration.

Set `MARKETING_ENABLED=false`, remove the marketing ArgoCD Application, and
remove the apex/`www` route to disable it without changing product code or the
product hostname. Existing adopter hostnames are never changed automatically.

Use `deployment/kustomize/overlays/aws/marketing-domains.example.patch.yaml` as
a copy-only domain and TLS example. Replace every host and certificate name;
configure exact authentication callback origins separately.

## Extraction contract

The complete source dependency graph is:

```text
marketing
  -> generated public site configuration (data only)
  -> repository Markdown content provider
  -> optional public plan HTTP projection
  -> configured product handoff origin
```

There are no reverse source dependencies from the generated public
configuration into either application. `node module/tools/marketing-extraction.mjs`
copies only this service to a temporary build context, installs from its own
lockfile, runs its tests and quality gates, builds with an unavailable product
API, verifies budgets, and starts the production server for smoke checks.

After moving this directory to another repository, preserve the generated
configuration file or replace it with an equivalent data-only generator output.
No product business logic or shared filesystem is required.
