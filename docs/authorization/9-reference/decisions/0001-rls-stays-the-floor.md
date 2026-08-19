# ADR-0001 — RLS stays the tenant floor

- **Status:** Accepted
- **Date:** 2026-08-19
- **Context:** Every capability we add (hierarchical scope, per-record sharing,
  a possible future ReBAC PDP, caching) introduces a layer above the database
  that decides finer-grained access. Those layers can be stale, buggy, or
  temporarily unavailable.

## Decision
Postgres forced row-level security on `org_id` remains the **fail-closed tenant
floor**. No feature ever moves tenant isolation above the database. All
finer-grained authorization composes **above** RLS and can only narrow, never
widen past it. This is invariant [I1](../../1-spec/invariants.md).

## Consequences
- A bug or outage in any higher layer (scope resolver, share overlay, PDP, cache)
  cannot cross a tenant boundary — the DB still filters.
- If we later adopt an external ReBAC PDP, it decides *within-tenant* fine-grained
  access only; the tenant answer is never delegated to it, and PDP↔DB consistency
  work never touches the floor.
- Every integration test for a new authorization feature must assert that a bug
  in that feature still cannot breach RLS.

## Why (not) alternatives
Delegating tenant isolation to an application PDP or a ReBAC service would make
tenant safety depend on cache freshness and service availability — unacceptable
for the one property that must never fail. Keeping it in the database, co-located
with the data, is the only place it holds regardless of query path.
