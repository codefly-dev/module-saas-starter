# Dynamic Dashboards — roadmap to chat-driven, user-owned dashboards

> **Goal:** a signed-in user **defines and changes a dashboard at runtime, from the
> FE, by conversation** ("show failed logins by day as a line") — and it **saves,
> is org-scoped, and is shareable**. No developer, no deploy.
>
> Tracking epic: **#369**. This continues the Dynamic Dashboards epic (#321), which
> delivered the *primitives*; this roadmap covers the *productization*.

## The one-paragraph state of the world

A dashboard is **serializable configuration, rendered generically, with data from
the backend audit engine** — config in, charts out. The config can originate from a
solution manifest (`solution-runtime-go` `Manifest.Dashboard` → `/.well-known/solution.json`,
host renders it), a committed JSON file, or an app-DSL literal (`/insights`). The
renderer is `@codefly/ui/dashboard` (`<Dashboard>` + charts); the data is the
accounts `AggregateAuditLog` engine, org-scoped and read-only. Audit event types are
now versioned (`auth.login.v1`, #362), so a declared metric binds real data.

The **runtime-editing primitives already exist and are tested** — but they are wired
to nothing, persistence is browser-local, and there is no user-owned-dashboard model,
no chat surface, and no dashboard authz. The gap to the goal is *productization, not
primitives.*

## Current state — DONE vs MISSING (audited)

| Capability | State | Where |
| --- | --- | --- |
| Serializable, versioned spec + validation | **DONE** | `features/dashboard/model/{schema,validate}.ts` |
| In-memory runtime edit + persistence | **DONE — localStorage only** | `service/use-dashboard-draft.ts` |
| Guard-railed authoring API (list / preview / commit) | **DONE — read-only vs audit; commit → local draft** | `service/{authoring,use-dashboard-authoring}.ts` |
| Pure canvas renderer(s) | **DONE** | `features/dashboard/ui/dashboard.tsx`; `@codefly/ui/dashboard` |
| NL → spec driver | **STUB only** (regex, no model) | `service/dashboard-driver.ts` |
| Live, versioned audit data to bind to | **DONE** | audit registry versioned (#362) |
| Authoring loop wired into a real UI | **MISSING** | no page consumes the primitives |
| Server-side dashboard persistence | **MISSING** (design-only `DashboardDraftStore`) | — |
| User-owned dashboard model + CRUD + sharing | **MISSING** | — |
| Dashboard create/edit/share authz | **MISSING** (only `audit:read` for viewing) | — |
| External-driver channel (host exposes; robin drives) | **MISSING** | — |
| `<Chat>` / AG-UI / NL→spec agent | **NOT IN THIS REPO — by design** (lives in `obin-ai/module-robin`) | — |

## The path (epic #369)

1. **#364 — Wire the authoring loop into a live Dashboards surface.** The primitives
   are wired to nothing; put the draft ↔ authoring ↔ `<Dashboard>` loop on screen as
   a structural editor (no chat). *Unblocks everything; ships on localStorage.*
2. **#365 — Server-side persistence** (`DashboardDraftStore` → a real store, behind the
   existing hook API). *The central gap.* **Decision:** a dedicated dashboards store
   (proto + JSONB `spec`, org-scoped, RLS) is preferred over a settings-service slot,
   because dashboards are first-class objects with their own lifecycle and sharing.
3. **#366 — User-owned dashboard model + CRUD.** A dashboard record
   `{id, orgId, ownerId, name, spec, visibility, …}` + `Create/Get/List/Update/Delete/Share`.
   This is where **user dashboards diverge from solution dashboards**: the *spec* shape
   is shared; the *lifecycle* (runtime records, ownership) is new.
4. **#367 — Dashboard authz.** New `dashboards:read/write/share` scopes, enforced at the
   handler layer, plus a hard **read-only-spec invariant**: a user- or chat-authored spec
   can only ever compile to org-scoped, read-only `AggregateAuditLog` — asserted at compile
   time so a hostile spec is inert.
5. **#368 — External-driver channel** (the seam, **not** a chat). The host exposes a clean
   way for a **composing module** to change a live dashboard — preferably a **store
   subscription**: the canvas subscribes to the persisted user-dashboard record (#366) and
   re-renders whenever an external caller updates it via the `UpdateDashboard` RPC
   (decoupled, driver can be server-side), with an optional injected-authoring-handle seam
   for low-latency preview. **The host mounts no `<Chat>`, no AG-UI, no agent.** The stub
   driver stands in for the external caller in tests.

### Sequencing
`#364` (loop on screen) → `#365` (durable) → `#366` (first-class objects) → `#367`
(gated) → `#368` (external-driver channel). Each ships independently; the demo is always live.

## Boundary (obin ↔ codefly) — the chat is NOT in this repo

The **host** (this repo) owns the canvas, the authoring API, persistence, the
user-dashboard model, authz, and the **external-driver channel** — the mechanics of
changing a dashboard. It hosts **no chat, no AG-UI, no NL, no agent**. The **chat surface
and the NL→spec intelligence live entirely in `obin-ai/module-robin`** (epic #16: #13 agent
tools over the host API, #14 NL→spec turn, #15 the `<Chat>` surface), **mounted in the
solutions layer** and driving the host's channel. The host names nothing obin-specific and
exposes only the programmatic capability; the dependency direction stays **obin → codefly,
never the reverse.** #368 exposes the channel; robin brings — and hosts — the chat.

## Why it's cheap once the primitives are productized

The dashboard is already pure data with a validator and a generic renderer, the backend
audit engine already exceeds what the UI exposes, and the driver seam means the whole
"describe → mutate → validate → render" loop is provable with zero model calls before any
agent is involved. Persistence + a user model + authz + a chat mount turn the existing
primitives into a product.

## References

- `DYNAMIC_DASHBOARDS.md` — the primitives epic (#321).
- `DASHBOARD_AUTHORING_DESIGN.md` — the authoring API + the design-only `DashboardDraftStore`.
- `#362` — audit event types versioned (`auth.login.v1`), so declared metrics bind live data.
- `@codefly/ui/dashboard` — the shared pure renderer; `@codefly/saas-sdk` `runDashboard` — the resolver.
