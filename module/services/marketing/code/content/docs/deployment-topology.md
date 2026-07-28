---
type: doc
slug: deployment-topology
title: Deploy the public site and product independently
description: Domain, cache, release, rollback, disablement, and mixed-platform guidance.
locale: en
state: published
revision: deployment-topology-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - deployment
  - extraction
navigationOrder: 20
category: operations
affectedFeatures: []
---
## Canonical domains

- The apex redirects to the canonical `www` host.
- `www` serves the marketing service.
- `app` serves the authenticated frontend through the authentication gateway.
- `status` is externally hosted and never routed through either application.

Production domain examples are configuration templates. Replace every `example.com` value and add exact authentication callback origins before launch.

## Independent releases

Marketing owns its build output, runtime environment, health endpoint, release identifier, cache behavior, smoke test, and ArgoCD application. Product deployment does not wait for marketing, and marketing readiness does not fail when the public catalog is unavailable.

Roll back each deployment to its previous image revision. The public service stores no database state, so its rollback does not require a product migration.

## Disable marketing

Set `MARKETING_ENABLED=false` to make the service return an explicit disabled page. Existing adopters can also omit the marketing ArgoCD application and apex or `www` route while leaving the product service and hostname unchanged.

## Different platforms

The only runtime integration is an HTTPS public-catalog URL and fixed product handoff origin. Deploy marketing on a static or server-rendering platform and the authenticated product on Codefly without a shared filesystem or same-host requirement.
