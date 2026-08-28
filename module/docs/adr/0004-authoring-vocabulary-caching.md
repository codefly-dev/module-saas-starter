# ADR 0004: Caching strategy for the imperative authoring surface — one vocabulary cache, no aggregate cache

- Status: Proposed
- Date: 2026-08-27
- Task: closes the decision deliverable of #332, raised by the review of #325
  (the #320 authoring seam). It authorizes the refactor described below but does
  not change code: the target lives in #325, which is not yet on `main`.

## Context

#320 (PR #325) adds the driver-facing authoring seam for the Dynamic Dashboards
epic (#321): `createDashboardAuthoring`, a plain-object contract an external
driver — a form editor, a test harness, or a conversational agent that composes
this module — binds to in order to `listEventTypes`, `previewMetric`, and
`setDashboard`. It is deliberately React-free so a non-React driver can bind it;
the React app wires it through `useDashboardAuthoring()`.

Both `listEventTypes` and validation (inside `previewMetric`/`setDashboard`)
need the **audit event vocabulary** — the server-owned registry of event types
and their categories. Because the contract sits below React, it cannot use the
react-query hook (`useAuditEventTypes`) the rest of the app uses. To avoid
re-fetching the registry on every preview/commit, #325 memoizes it **per
authoring instance**: a promise cached inside the closure returned by
`createDashboardAuthoring`, cleared on a failed fetch so a transient error is
retryable. `useDashboardAuthoring` recreates the instance when `audit` or
`orgId` changes, so staleness is bounded to the life of one instance.

That memo is correct and consistent with how the registry is treated elsewhere.
But it is a **second cache** for the same registry that react-query already
caches under the query key `["audit-event-types"]` with a 5-minute `staleTime`
(`features/audit/service/queries.ts`). The #325 review asked whether the app
should maintain two caches for one deploy-static registry long-term. This ADR
is the answer.

### Baseline (what exists today)

- **react-query cache** — `useAuditEventTypes()` in
  `features/audit/service/queries.ts`: `queryKey: ["audit-event-types"]`,
  `queryFn: () => svc.listAuditEventTypes({})`, `staleTime: 5 * 60 * 1000`, and
  a `select` that projects the wire types into `AuditEventTypeInfo[]`. The one
  app-wide `QueryClient` (`lib/providers.tsx`) defaults queries to a 30s
  `staleTime`; this hook overrides it to 5 minutes.
- **Per-instance memo** — inside `createDashboardAuthoring`
  (`features/dashboard/service/authoring.ts`, on the #325 branch): a
  closure-scoped `eventsPromise` filled by the same `listAuditEventTypes` call
  and the same projection, cleared to `undefined` on rejection.
- **Aggregate reads** — `previewMetric` calls `aggregateAuditLog` on every
  invocation with no caching of the result.
- **Draft persistence** — the committed spec is written to `localStorage` via
  `DashboardDraftStore`, injected into the authoring instance. #321 will move
  this to the settings service (`@codefly/saas-settings`).

### What shapes the decision

- The registry is **deploy-static**: it changes only when a deploy registers or
  retires an event type. Within a session it does not move.
- Aggregate results are **not** static: they grow continuously as audit events
  accrue. A preview's whole contract is "see real, current data before you
  commit."
- The authoring core is **host-agnostic by design** (#320's external-driver
  goal). Whatever we do must not couple `createDashboardAuthoring` to react-query
  or to React.
- The vocabulary cache (a server-owned *read* cache) and draft persistence (a
  user-owned *write* store) are already separate injected concerns.

## Decision

**Stop giving the authoring core its own registry cache. Make the vocabulary
read an injected dependency and, in the React app, back it with react-query's
existing `["audit-event-types"]` cache — so the app has one vocabulary cache and
one invalidation surface. Do not cache aggregate preview results. Keep the
vocabulary cache and draft persistence as independent injected seams, so the
#321 settings-service move touches neither the core nor the vocabulary path.**

### Q1 — Share react-query's cache? Yes, by injection, not by coupling

The core stops owning a cache. `createDashboardAuthoring` takes an injected
reader instead of memoizing internally:

```ts
export interface DashboardAuthoringDeps {
  // The vocabulary read is injected; the core no longer fetches or caches it.
  readEventTypes: () => Promise<AuditEventTypeInfo[]>;
  // Narrowed from #325's `audit` dep: the core still issues aggregate reads
  // directly (uncached — see Q3); it just no longer calls listAuditEventTypes.
  audit: Pick<Client<typeof AuditService>, "aggregateAuditLog">;
  store: DashboardDraftStore;
  orgId: string;
}
```

`listEventTypes`, `previewMetric`, and `setDashboard` all call `readEventTypes()`
and never touch a cache themselves. This is the **entire `Deps` delta** from
#325: `listAuditEventTypes` leaves the injected `audit` client and becomes
`readEventTypes`; `aggregateAuditLog` stays exactly as it was, under the same
`audit` dep. No new aggregate abstraction is introduced. Each host supplies the
reader its environment already has:

- **The app (`useDashboardAuthoring`)** injects a reader backed by react-query,
  so there is exactly one cache:

  ```ts
  const readEventTypes = () =>
    queryClient.fetchQuery(auditEventTypesQuery(svc));
  ```

  `fetchQuery` returns the cached value when it is fresh, refetches when it is
  stale, and **dedupes against the in-flight `useAuditEventTypes` query** because
  it shares the key — one fetch, one cache, one invalidation point.

- **A non-React driver** injects whatever cached reader (or none) its host uses.
  The core neither knows nor cares.

To keep the two app consumers from drifting on the key, `staleTime`, or
projection, factor the query definition into one exported descriptor and have
both `useAuditEventTypes` and the injected reader consume it. The projection
**must live in `queryFn`, not `select`**: `select` is a `useQuery`-only observer
transform, and `queryClient.fetchQuery` does not apply it (react-query v5). A
descriptor that carried the projection in `select` would leave the imperative
reader resolving to the raw `ListAuditEventTypesResponse` (`{ types }`), not
`AuditEventTypeInfo[]` — its declared return type would be a lie and the first
`events.map(…)` downstream would throw. Put the projection where both entry
points see it:

```ts
export const auditEventTypesQuery = (
  svc: Pick<Client<typeof AuditService>, "listAuditEventTypes">,
) => ({
  queryKey: ["audit-event-types"] as const,
  queryFn: async (): Promise<AuditEventTypeInfo[]> =>
    toAuditEventTypeInfo(await svc.listAuditEventTypes({})),
  staleTime: 5 * 60 * 1000,
});
```

`useAuditEventTypes` then becomes `useQuery(auditEventTypesQuery(svc))` with **no
`select`** — its data is already projected — and `readEventTypes` above resolves
to `Promise<AuditEventTypeInfo[]>` correctly. Moving the projection into
`queryFn` changes the *cached* shape under `["audit-event-types"]` from the raw
response to `AuditEventTypeInfo[]`, so the two edits (queryFn projection +
dropping the hook's `select`) must land together, never half. The only current
reader of that entry is `useAuditEventTypes`; nothing reads the raw entry via
`getQueryData`, so the shape change is contained.

This is also a **new access pattern** for the app: it uses `invalidateQueries`
today but no `fetchQuery`/`ensureQueryData`, so the `select` caveat above is easy
to miss — hence spelling it out and pinning the projection to `queryFn`.

We reject injecting a `QueryClient` (or react-query itself) into the core: it
would couple the host-agnostic contract to one framework and complicate the
plain-object test harness for no gain. The reader function is the seam.

The per-instance memo — including its clear-on-failure retry behaviour — then
disappears from the core. react-query already owns retry (`retry: 1` on the
client) and staleness. The core becomes a thin, cache-free translator of driver
calls into reads.

### Q2 — Invalidation: 5-minute staleness, one shared key

The registry is deploy-static, so 5 minutes of staleness is comfortably safe and
is the value the app already lives with. Unifying makes this **strictly better**
than the memo: react-query refreshes a stale entry on the next read, whereas the
per-instance memo never refreshes until `useDashboardAuthoring` recreates the
instance (on an `audit`/`orgId` change) — so a long-running agent session on a
stable org could hold a vocabulary snapshot indefinitely.

We add no explicit deploy-time invalidation now. If a deploy/version signal ever
warrants it, a single
`queryClient.invalidateQueries({ queryKey: ["audit-event-types"] })` refreshes
**both** consumers at once — which is the point of collapsing to one key.

### Q3 — `previewMetric` aggregate caching: do not cache

Leave aggregate reads uncached. Unlike the vocabulary, aggregate results are not
deploy-static — they change with every recorded event. Caching them would trade
the one property a preview must have (it reflects live data) for a small latency
win, and would show an interactive or agent driver **stale counts** the longer a
session runs. Repeated previews are plain org-scoped read RPCs and are cheap;
the correctness cost of caching outweighs it.

If a specific driver ever needs to dedupe genuinely-identical in-flight previews
(e.g. rapid re-preview of the exact same `MetricDef`), that belongs in the host's
injected `aggregate` reader — the same DI seam as the vocabulary reader — not in
the core, and not as a persistent result cache. This keeps the two caching
concerns cleanly split: **vocabulary = long-lived shared cache; aggregates =
always live.**

### Q4 — localStorage → settings service: orthogonal

The #321 move from `localStorage` to `@codefly/saas-settings` is unrelated to
vocabulary caching. The vocabulary cache is a server-owned *read* cache
(react-query); the draft store is a user-owned *write* store injected as
`DashboardDraftStore`. They share no state and no invalidation. The settings
move swaps only the injected store implementation; it does not touch the
vocabulary reader or the core. The DI shape decided here — inject the store,
inject the vocabulary reader — is exactly what lets both evolve independently.

### Options considered

- **A. Keep the per-instance memo (status quo of #325).** Correct and simple,
  but leaves two caches for one registry, and never refreshes within an
  instance's life. Rejected as the long-term state; retained only until the
  refactor lands (see Consequences).
- **B. Inject a `QueryClient` into the core and call `fetchQuery` there.**
  Unifies the cache but couples the host-agnostic contract to react-query and
  React, defeating #320's external-driver goal and complicating tests. Rejected.
- **C. Inject a `readEventTypes` reader; app backs it with react-query.**
  (Chosen.) One app cache, host-agnostic core, unchanged test ergonomics (inject
  a fake reader).
- **D. Also cache aggregate results.** Rejected — previews must be live
  (see Q3).

## Consequences

- **One vocabulary cache in the app.** `useAuditEventTypes` and the authoring
  surface read the same react-query entry; a future invalidation hits both. The
  core owns no cache and no retry logic.
- **Host-agnostic core preserved.** `createDashboardAuthoring` depends only on
  injected functions, so an external/non-React driver still binds it, and the
  test harness still passes a fake reader — no `QueryClient` in unit tests.
- **Previews stay honest.** Aggregate reads remain live; no new staleness class
  is introduced for the data a driver commits against.
- **#321 unblocked cleanly.** The settings-service persistence swap is confined
  to the injected `DashboardDraftStore`.
- **Sequencing.** Like ADR 0003, this ADR authorizes but does not perform the
  change. The target (`createDashboardAuthoring`) lives in unmerged PR #325, so
  the refactor lands either folded into #325 during review or as a follow-up once
  #325 is on `main`. Until then the per-instance memo is correct and stays. The
  shared `auditEventTypesQuery` descriptor in `features/audit/service/queries.ts`
  can land independently first to shrink that follow-up, but it is **not** a pure
  addition: it moves the projection into `queryFn` and drops `select` from
  `useAuditEventTypes` in the same change (see Q1), which alters the cached shape
  under `["audit-event-types"]`.
