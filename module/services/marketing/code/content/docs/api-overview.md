---
type: doc
slug: api-overview
title: Customer API overview
description: How customers reach canonical API documentation without exposing platform-only methods.
locale: en
state: published
revision: api-overview-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - api
navigationOrder: 40
category: developers
affectedFeatures: []
---
## Contract authority

The authenticated product renders its checked-in OpenAPI contract instead of duplicating endpoint prose in this public service. Internal and platform-only procedures remain excluded by the generated REST surface policy.

## Authentication and tenancy

Customer requests use the authentication methods, scopes, tenant semantics, rate limits, request IDs, retries, and idempotency rules declared by the canonical service catalog.

Open the configured [product application](https://app.example.com) to view authenticated API documentation and API-key management.

## Examples

Examples must use placeholders and never include a live key.

```bash
curl -H "Authorization: Bearer [redacted]" \
  https://app.example.com/v1/.well-known/service-info
```
