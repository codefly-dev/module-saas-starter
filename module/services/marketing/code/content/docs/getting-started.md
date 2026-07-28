---
type: doc
slug: getting-started
title: Configure the public launch shell
description: Replace development fixtures, connect domains, and verify the independent public service.
locale: en
state: published
revision: getting-started-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - launch
navigationOrder: 10
category: setup
affectedFeatures: []
---
## Replace public configuration

Edit the canonical public site configuration under `module/public`. Replace the fixture company, product, descriptions, domains, contacts, colors, typography, locales, and acquisition mode. Keep customer and testimonial lists empty unless you have evidence and permission to publish each claim.

Regenerate the two checked-in projections with the repository public-configuration generator. Marketing and product authentication then share the safe brand identity without sharing tenant branding or runtime state.

## Connect the public catalog

Set `MARKETING_CATALOG_URL` to the accounts service public origin. The pricing page requests only the sanitized public plan route and sends no credentials or cookies.

If the catalog is unavailable, pricing reports that state and does not invent or duplicate prices.

## Verify before launch

Run the production readiness report. It rejects development fixture values, placeholder contacts, non-canonical domains, a non-indexable production policy, and disabled marketing.

Then run the extraction build and independent smoke test described in the [extraction guide](/docs/extraction).
