---
type: use-case
slug: independent-launch
title: Launch public content without coupling product releases
description: Ship company pages, documentation, and release content on their own cadence.
locale: en
state: published
revision: use-case-independent-launch-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - launch
navigationOrder: 10
category: operations
affectedFeatures: []
---
## Situation

A team needs to publish a company announcement or documentation correction without rebuilding the authenticated application.

## Starter contract

The marketing service owns its build output, health check, release identifier, cache policy, and deployment target. Product sessions and tenant logic never enter its dependency graph.

## Failure behavior

If the public plan API is unavailable, pricing becomes an honest degraded state. Published pages, feeds, metadata, and documentation remain available.
