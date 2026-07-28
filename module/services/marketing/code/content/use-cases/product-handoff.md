---
type: use-case
slug: product-handoff
title: Hand public acquisition into an authenticated product
description: Preserve approved attribution without accepting unsafe redirect or session state.
locale: en
state: published
revision: use-case-product-handoff-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - acquisition
navigationOrder: 20
category: product
affectedFeatures: []
---
## Situation

A visitor moves from landing or pricing into signup, waitlist, invitation, or login.

## Starter contract

The public service selects one configured acquisition mode. It builds links from an exact product origin and fixed path, copies only approved campaign fields, bounds every value, and validates the optional plan key.

## Ownership

Waitlist state, invitation acceptance, consent association, onboarding, analytics identity, and notification delivery remain in their owning product services. Marketing does not create parallel state.
