---
type: article
slug: separate-runtime
title: Why the public site runs separately
description: A practical boundary between public company content and authenticated product state.
locale: en
state: published
revision: article-separate-runtime-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-01T09:00:00.000Z
updatedAt: 2026-07-01T09:00:00.000Z
tags:
  - architecture
  - launch
navigationOrder: 10
category: architecture
affectedFeatures: []
---
## Availability is part of the product

A company site and an authenticated application have different traffic, cache, release, and failure characteristics. Running them as separate services lets public content remain available when an authenticated dependency is degraded.

The marketing service does not import product sessions, organization state, database stores, dashboard packages, or internal administration clients.

## Narrow handoff

Calls to action use a configured product origin and fixed paths. Only an approved attribution allowlist can cross the boundary; arbitrary return URLs are never accepted.

Read the [deployment topology](/docs/deployment-topology) or inspect [pricing](/pricing) to see the boundary in practice.
