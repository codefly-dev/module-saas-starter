# Dashboard authoring API — design decisions

> The driver-facing authoring seam (`listEventTypes` / `previewMetric` /
> `setDashboard`, #320 / PR #325) had to make three calls under constraints:
> #317 was not yet on `main`, the result contract was shaped for simplicity,
> and caching was done the minimal way. Each is defensible, and each came out
> of the PR review flagged as "revisit before this surface is done" (#333,
> under the Dynamic Dashboards epic #321).
>
> **This document makes those three decisions deliberately and records the
> rationale.** It is a design document, not shipped code: it takes a position on
> the ownership/layout question (#330), the result-contract shape (#331), and
> the caching strategy (#332), and marks which decisions imply a follow-up
> change versus ratify what already exists. #331 (the external driver contract)
> has since shipped — §2 records the shape that landed, not a proposal. #330 and
> #332 still change code (#323 is an open PR, and #320/#325 has since merged into
> the baseline they reconcile against), so those two remain proposed decisions
> that bind the reconciliation — not a settled record that overrides those PRs'
> authors.

This is the "reflect on the seam we just built" companion to #321's "build the
seam" children. Nothing here is blocking — the authoring API works today and
its guard rails are hardened. The point is to avoid drift before the surface is
declared done.

All paths below are under
`module/services/frontend/code/src/features/dashboard/` unless noted, and were
verified against the tree: `model/` and `service/use-dashboard-draft.ts` are on
`main` (#317, merged as #328); `service/authoring.ts`,
`service/validation.ts`, `service/draft-store.ts`, and
`service/use-dashboard-authoring.ts` are in the open PR #325 (#320).

---

## 1. Draft store + validator ownership (#330)

### What exists, and the three-way divergence risk

Two concepts — a draft store and a validator — are about to exist in up to
three shapes across #317 (`main`), #320 (PR #325), and #323 (the dev harness).

**Validators.** They are not duplicates of one job; they guard two different
boundaries with two different contracts:

- `model/validate.ts` — `assertDashboardSpec` / `parseDashboardSpec`
  (#317, on `main`). **Throwing, spec-internal.** It narrows an untyped value
  to `DashboardDef`, enforces the version discriminant, exact-key shape, and
  dimensional coherence (a time metric carries a bucket; a categorical `limit`
  never lands on a time series). It is the guard at the **render / deserialize**
  boundary — a bad spec from code, localStorage, or a driver must never reach
  `<Dashboard>`. It knows nothing about which event types actually exist.
- `service/validation.ts` — `validateMetric` / `validateDashboard`
  (#320, PR #325). **Non-throwing, accumulating, registry-aware.** It returns
  every `FieldError` at once and is the only one that checks a metric against
  the live audit vocabulary (`unknown_event_type`, `unknown_category`). It is
  the guard at the **authoring** boundary — an external driver composing a spec
  needs all its mistakes back as data in one pass, including "that event does
  not exist," which `assertDashboardSpec` cannot know.

The overlap is real but narrow: both encode the same enum sets (`GROUP_BY`,
`BUCKET`, `CHART`) and the same coherence rules (bucket-requires-time, the
`limit` rules). That duplicated rule set is the divergence risk — the two can
drift so a spec passes one and fails the other.

**Draft stores.** Same pattern — two consumers, two shapes:

- `service/use-dashboard-draft.ts` — `useDashboardDraft` (#317, on `main`). A
  React hook: `{ spec, setSpec, reset, error }`, localStorage-backed, validates
  every spec that enters via `assertDashboardSpec`, and surfaces failures
  through `error` rather than a thrown render. It is what a **React component**
  binds to.
- `service/draft-store.ts` — `DashboardDraftStore` (#320, PR #325). A
  framework-agnostic interface (`load` / `save` / `clear` / `subscribe`) with
  browser and in-memory implementations, driving `useSyncExternalStore`. It is
  what the **imperative authoring API** (`createDashboardAuthoring`) and a
  cross-tab-shared `<Dashboard>` bind to — neither of which can hang off a
  single component's hook state.

### Decision

**Keep two validators and two draft stores, but give each concept one
canonical owner of its shared rules, and let the other layer on top rather than
restate.** They are not redundant; collapsing either pair would force one
boundary to adopt the other's contract (throw-vs-accumulate, registry-aware-vs-
not, hook-vs-external-store), which is strictly worse.

- **Canonical validator core: `model/validate.ts`.** The enum sets and the
  dimensional-coherence rules live there once. `service/validation.ts` keeps
  its non-throwing `FieldError` contract and its registry checks
  (`unknown_event_type` / `unknown_category`, which are its alone), but stops
  re-encoding the coherence rules — it derives them from the model layer so a
  rule changes in exactly one place. Concretely, the shareable unit is the
  enum sets (`model/`'s `GROUP_BY`/`BUCKET`/`CHART`, which
  `service/validation.ts` currently re-declares as `GROUP_BYS`/`BUCKETS`/
  `CHARTS`) and the bucket/`limit` coherence rules extracted as pure predicates.
  It is only those data and predicates that move to `model/`, not the control
  flow: `model/validate.ts` keeps throwing on the first violation while
  `service/validation.ts` keeps accumulating every `FieldError`, each consuming
  the shared predicates in its own way. That leaves `service/validation.ts`
  owning only vocabulary resolution and the `FieldError` projection.
- **Canonical draft store: the framework-agnostic `DashboardDraftStore`
  (`service/draft-store.ts`).** It is the more general object — the imperative
  API and any number of subscribers can share one store; a hook cannot back
  that. `useDashboardDraft` is re-expressed as a thin `useSyncExternalStore`
  wrapper over the canonical store (this is exactly what
  `use-dashboard-authoring.ts` already does with its `useDashboardDraft`
  export), rather than a second, independently-validating localStorage
  implementation. This collapses a live name collision, not just a duplicated
  concept: `main`'s `index.ts` re-exports a `useDashboardDraft` (returning the
  `DashboardDraft` control object) from `service/use-dashboard-draft.ts`, while
  #325's `use-dashboard-authoring.ts` exports a *different* `useDashboardDraft`
  (returning `DashboardDef | null`). Two symbols, one name — `index.ts` can only
  re-export one, so the reconciliation must pick the single wrapper deliberately
  rather than let a merge decide. The `model/`-owned validator runs inside the
  store's `save` path so persistence and authoring share one notion of "valid."
- **Paths.** Keep `model/` for spec + validation and `service/` for the store,
  matching #317's merged layout. When #320 lands, its `service/validation.ts`
  stays (registry-aware layer) and its `service/draft-store.ts` becomes the one
  store; #317's `service/use-dashboard-draft.ts` collapses to the thin wrapper.
  Do **not** introduce a third `model/validate.ts` from #323 — the dev harness
  must import the canonical model, not fork it.

### Follow-up

This implies a small reconciliation change **when #320 (PR #325) lands, and
before #323 merges its own `model/validate.ts`** — not now, since the #320 code
is not yet on `main`. The change: export the coherence constants/predicates
from `model/`, have `service/validation.ts` consume them, and land a single
draft store with the hook as a wrapper. The #323 review should reject a forked
validator/draft-store and point it at the canonical modules.

---

## 2. `PreviewResult` / `CommitResult` shape (#331)

### The problem

`previewMetric` returns one flat failure channel:

```ts
type PreviewResult =
  | { ok: true;  preview: MetricPreview }
  | { ok: false; errors: FieldError[] };
```

Two semantically different failures ride that one channel:

1. **Spec-validation** — `unknown_event_type`, `invalid_group_by`, … The driver
   fixes these by editing the spec.
2. **Precondition / transient app-state** — `org_unresolved`: no organization
   is in scope yet, so the org-scoped aggregate cannot run. The driver **cannot
   fix this by editing the spec**; it must wait and retry.

Today they are distinguishable only by a magic `code` string on a
`FieldError` whose `path` is the pseudo-field `"orgId"` — a driver must
special-case that token to tell "fix your spec" from "wait for context."

### Decision

**Split the precondition signal out of the validation channel** as a distinct
variant on `previewMetric`:

```ts
type PreviewResult =
  | { ok: true;  preview: MetricPreview }
  | { ok: false; kind: "validation"; errors: FieldError[] }
  | { ok: false; kind: "pending"; code: string; message: string };
```

The two failures are not the same kind of thing, and the type should say so.
`FieldError[]` is a contract about **spec locations a driver can fix**;
`org_unresolved` has no spec location and nothing to fix. Folding it into
`errors` forces every driver to know that one `code` is really a control-flow
signal, not a field error — the exact "special-case string codes" tax the
review flagged. A discriminated `kind` makes "fix your spec" versus "wait for
context" a type distinction the compiler enforces, and removes the fake
`path: "orgId"` `FieldError`.

The `pending` arm keeps a `code`/`message` — the same pair a `FieldError`
carries, minus `path` (the block is not a spec field). `kind` answers "fix
your spec vs. wait for context"; `code` answers "wait for *what*." `org_unresolved`
is the only precondition today, but a second one would otherwise force a driver
back to substring-matching the prose `message` — reintroducing the string-code
tax one level down. A stable machine-branchable `code` keeps the human sentence
(`message`) for display without making it load-bearing.

**`CommitResult` stays two-variant** — deliberately asymmetric:

```ts
type CommitResult =
  | { ok: true;  spec: DashboardDef }
  | { ok: false; kind: "validation"; errors: FieldError[] };
```

`setDashboard` validates the spec and writes the local draft store; it does
**not** touch the org-scoped aggregate RPC, so it has no precondition that can
be "pending." Giving it a `pending` variant it can never return would be
defensive shape for a state that cannot happen. Adding the `kind: "validation"`
discriminant to both keeps the surface consistent to read and pattern-match,
while only `previewMetric` — the one operation that reads org-scoped data —
carries `pending`. The asymmetry is the honest shape: it tracks which
operations actually have a precondition.

### Follow-up

**Implemented (#331), now that #320 has landed on `main`.** `service/authoring.ts`
carries the `kind` discriminant on both result types; the `orgId === ""` guard
returns `{ ok: false, kind: "pending", code: "org_unresolved", message }` instead
of the synthetic `path: "orgId"` `FieldError`. Callers switch from sniffing
`errors[0].code === "org_unresolved"` to `result.kind === "pending"`, and read
`result.code` when they need to tell one precondition from another.

---

## 3. Caching strategy for the imperative surface (#332)

### What exists

Two caches hold the same server-owned audit event registry:

- `useAuditEventTypes` (the audit **hook**) caches it through react-query with a
  5-minute `staleTime`.
- `createDashboardAuthoring` (the imperative **authoring** API) memoizes the
  vocabulary **per instance** in a `readEvents()` promise, clearing it on a
  failed fetch so a transient error is retryable.
  `useDashboardAuthoring` rebuilds the instance whenever `audit` or `orgId`
  changes, so staleness is bounded to the instance's lifetime.

### Decision

**Keep the per-instance memo. Do not route the imperative surface through
react-query.** The authoring API is deliberately a plain `Promise`-returning
contract an **external driver** binds to — it is not a hook and does not run
inside React's render tree. react-query's cache is reached through hooks
(`useQuery`) whose lifecycle is the component tree; making the driver share it
would couple a framework-agnostic contract to React's render lifecycle and
invalidation model, which is the opposite of what an external, possibly
agent-driven caller wants. Two caches of a **deploy-static** registry, each
treating it as effectively immutable within a session, is an acceptable cost;
one source of truth is not worth coupling the driver to react-query to get it.

Three sub-decisions:

- **Vocabulary invalidation.** The registry changes only on deploy, but a
  driver may run a long session, so an unbounded per-instance memo can pin a
  pre-deploy vocabulary indefinitely. Bound it: the memo should expire on the
  same order as the hook's `staleTime` (≈5 min) — a TTL on the cached promise,
  or an explicit `refresh()` on the authoring contract the driver can call at
  the start of a work unit. Match the hook's window so the two caches don't
  disagree about how fresh "fresh" is. The current unbounded memo is the one
  part of the status quo worth changing.
- **Aggregate-result caching.** **Do not cache aggregate results in
  `previewMetric`.** A preview's entire purpose is to show *live* data before a
  commit; a driver that previews the same metric twice and gets a stale count
  the second time is being misled at the exact moment it decides whether to
  commit. Re-running the aggregate is cheap relative to that correctness cost,
  and the registry memo already spares the expensive-to-list part. Preview
  stays uncached.
- **Settings-service migration.** When the draft moves from localStorage to the
  settings service (`@codefly/saas-settings`) per the #321 boundary, that is the
  **persistence** layer, orthogonal to the **vocabulary** cache decided here.
  The `DashboardDraftStore` interface (#330) already abstracts persistence, so
  that migration swaps a store implementation and does not touch the caching
  decision above.

### Follow-up

One bounded change **when #320 (PR #325) lands**: add a TTL or explicit
`refresh()` to the vocabulary memo in `createDashboardAuthoring` so a
long-lived driver session cannot pin a pre-deploy registry. Ratify the rest:
no react-query coupling, no aggregate-result cache, persistence migration
handled by the store interface.

---

## 4. Summary

| # | Decision | Status |
|---|----------|--------|
| #330 | Two validators / two draft stores, each with **one canonical owner of shared rules**: coherence rules live in `model/`, the framework-agnostic `DashboardDraftStore` is the canonical store, hooks and the registry-aware validator layer on top. No third fork from #323. | Ratify + reconcile whichever of #323/#325 lands first |
| #331 | Split `org_unresolved` into a distinct `kind: "pending"` variant on `PreviewResult` (carrying a machine-branchable `code`/`message`); add a `kind: "validation"` discriminant to both results; `CommitResult` stays two-variant because commit has no org-scoped precondition. | Implemented (#331) |
| #332 | Keep the per-instance vocabulary memo (no react-query coupling); **bound it with a TTL/`refresh()`**; never cache aggregate results; settings-service migration is a store-implementation swap. | Ratify + bound the memo when #320 lands |

Sequencing matters and is not a single gate. The #331 and #332 follow-ups were
gated on #320 (PR #325) merging; it has since landed on `main`, and #331 is now
implemented, leaving #332 (bound the vocabulary memo) as the open follow-up. The
#330 follow-up is **not** gated the same way: #323 (the dev harness) is an
independent open PR that
touches the on-`main` `model/validate.ts` and `service/use-dashboard-draft.ts`
directly, so it can land **before** #325 and move the "canonical `model/`" this
whole decision rests on. Whichever of #323/#325 lands first must be held to the
ownership decided here — #323 pointed at the canonical `model/` rather than a
fork, #325 reconciled onto it — or the baseline shifts under the other.

These are proposed decisions, recorded to keep the review's reasoning from
being lost; they bind the #323/#325 reconciliation but are not a substitute for
sign-off from those PRs' authors, whose code #330–#332 change.
