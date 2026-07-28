---
type: doc
slug: security-boundaries
title: Public service security boundaries
description: The enforced import, environment, network, cache, and handoff rules for marketing.
locale: en
state: published
revision: security-boundaries-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - security
navigationOrder: 50
category: trust
affectedFeatures: []
---
## Source boundary

The dependency gate rejects imports from accounts, authenticated frontend, dashboard, administration, database, store, vault, and organization business packages.

## Runtime boundary

Marketing configuration accepts public locations and release identifiers only. Credential-bearing URLs and secret-shaped configuration fields are rejected. Product API failure never changes the marketing readiness result.

## Browser boundary

Security headers deny framing, plugins, browser hardware capabilities, off-origin scripts, and off-origin form submission by default. The public service does not request or forward authentication cookies.

## Handoff boundary

Calls to action resolve against the configured product origin and fixed paths. Only the configured attribution field allowlist is copied, each value is length-bounded, and a plan must match the canonical key format.
