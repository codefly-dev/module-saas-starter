# ADR-0002 — Hierarchical scope via ltree + typed registry

> **Draft — proposed in #177, not yet accepted.** The decision below is a
> recommendation pending review; RFC-0001 is in **Review**. This becomes the
> immutable Accepted record only when the RFC is signed off.

- **Status:** Draft (proposed #177)
- **Date:** 2026-08-19
- **From:** RFC-[0001](../../2-proposals/0001-hierarchical-scope-ltree.md) (#177)
- **Context:** `role_assignments.scope` is a flat string matched by equality — no
  hierarchy, no precedence, no registry. Product needs "grant at a level, inherit
  down" (B4) with most-specific-wins (B5) over a per-tenant scope tree, without
  touching the RLS floor (I1).

## Decision
Add a **typed scope registry** (`scope_nodes`, an `ltree` materialized path with the
parent validated on write) and **hierarchical grants** (`scope_grants`: principal or
team + role at a node). A new **`CheckAccess`** resolver answers a record decision by
an `ltree` **ancestor match** (`grant.path @> record.path`), most-specific-wins,
resolving capability through the same `role_permissions` rows as RBAC (I6). It is
**strictly additive** — `role_assignments` / `CheckPermission` are untouched.

Sub-decisions:
- **Label encoding:** path labels are the node UUID with hyphens stripped (a valid
  `ltree` label on every supported Postgres, stable across renames). The human name
  lives in a separate `label` column.
- **Resolution site:** handler-side `CheckAccess` first; no RLS-side composition and
  no new principal GUC in v1.
- **Edit authority:** Owner + branch-delegated Admins (an `admin` grant at a node
  lets its holder create children and grant within that subtree, bounded by their
  own authority).
- **Team-scoped grants:** in v1 (`subject_kind = 'team'`), resolved via literal
  membership (no sub-team cascade — see ADR-linked TEAM-3).
- **Tree shape:** per-tenant and `kind`-typed, not a fixed four-level schema.

## Consequences
- Hierarchy ships without a parallel permission model; per-record sharing (ADR-0003)
  reuses the same resolver and vocabulary.
- New tenant relations (`scope_nodes`, `scope_grants`) must be classified forced-RLS
  with an exact grant matrix, and every new feature test must still prove RLS holds
  (I1, ADR-0001).
- Longer paths (32-char labels) at shallow depth; a compact synthetic key is a later
  optimization if measured to matter.

## Why (not) alternatives
Adjacency-list + recursive CTE puts recursion on the hot authz path; a closure table
is overkill for a strict tree; an external ReBAC PDP is the eventual graduation
target but pays a two-sources-of-truth consistency cost now. Materialized-path
`ltree` is one indexed ancestor operator, a single source of truth, and composes
with RLS — the right amount of machinery until nesting outgrows SQL (roadmap P3).
