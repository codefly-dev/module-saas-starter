# Plan

The host's functional contract is its handbook page, `modules/saas-starter.md`
in the umbrella docs repository: thirty-one `HOST-*` user stories over an
Interface block pinned to a release of this repository. This file is the only
plan here. It tracks that page story by story — nothing else, no phases, no
identifiers of its own — and it is kept current by the pull request that changes
a row's truth.

**Measure it, do not read it.** `node module/tools/story-trace-gate.mjs check
--page <path to the exported page>` compares the page and this tree in both
directions; CI runs the page-less half (`tests`) on every change. A row below
says `proven` only when a test named `TestStory_HOST_<AREA>_<NNN>` (Go, against
real Postgres) or `describe("HOST-<AREA>-<NNN> …")` (frontend) runs and passes on
`main`, never skipped. The tracker is one epic (#804) with one issue (#820); a
finding about any row is a comment or a checkbox there, never a new issue.

## Story ledger

Status: **proven** · **unproven** (behaviour believed present, no story test) ·
**implementation gap** (a test cannot pass until code changes). *Surface* names
the operations the proving test exercises, spelled as the page's Interface block
(pinned at `v0.0.55`) spells them — bare names, so `AddMember` and `Update`
read against the section the story sits in; it is what the page's `Surface:`
line has to carry once the handbook pull request lands. The `ENT` rows also
exercise whichever operation the plan gate ends up refusing; it is named once
the gate exists. A dash means the story exercises no
accounts operation and the page should say so.

| Story | Status | Proving test | Surface |
|---|---|---|---|
| HOST-ID-001 · Every request carries who and for whom | proven | `pkg/business/story_host_test.go` `TestStory_HOST_ID_001` | `StartTask` |
| HOST-ID-002 · An invitation is what makes someone a colleague | unproven | — | `CreateInvitation`, `AcceptInvitation`, `ListMembers`, `AddMember`, `CheckPermission` |
| HOST-ID-003 · A revoked invitation cannot be accepted | unproven | — | `CreateInvitation`, `RevokeInvitation`, `AcceptInvitation` |
| HOST-ID-004 · A key can do only what its scopes name | unproven | — | `CreateAPIKey`, `ValidateAPIKey`, `QueryAuditLog` |
| HOST-ID-005 · A revoked key does nothing | unproven | — | `CreateAPIKey`, `RevokeAPIKey`, `ValidateAPIKey` |
| HOST-ID-006 · An agent acts for a person, within bounds | unproven | — | `CreateAgentPrincipal`, `RequestDelegation`, `DecideDelegation`, `StartTask`, `CheckAccess`, `RenewWorkContext` |
| HOST-AUTHZ-001 · A capability I do not hold is refused | unproven | — | `AssignRole`, `CheckAccess`, `QueryAuditLog` |
| HOST-AUTHZ-002 · A role I grant takes effect | unproven | — | `CreateRole`, `AssignRole`, `RevokeRole`, `CheckAccess` |
| HOST-AUTHZ-003 · Another tenant's records are never returned | unproven | — | `GetDashboard`, `ListDashboards`, `GetSource`, `ListSources` |
| HOST-AUTHZ-004 · A service describes what it enforces | unproven | — | `GetServiceInfo` |
| HOST-JOB-001 · Work is never lost | proven | `pkg/infra/story_jobs_test.go` `TestStory_HOST_JOB_001` | `ClaimJobs`, `NackJob`, `ReplayJob`, `GetJob` |
| HOST-JOB-002 · The same work enqueued twice happens once | unproven | — | `EnqueueJob` |
| HOST-JOB-003 · The same key over different work is refused | unproven | — | `EnqueueJob` |
| HOST-AUD-001 · One audit spine, two views | proven | `pkg/business/story_host_test.go` `TestStory_HOST_AUD_001` | `EmitAuditEvent`, `QueryAuditLog` |
| HOST-APR-001 · Work waits for the decision it needs | unproven | — | `RequestApproval`, `GetApproval` |
| HOST-APR-002 · An approved request resumes the work | unproven | — | `RequestApproval`, `GetApproval`, `ClaimJobs` |
| HOST-APR-003 · A refused request never happens | unproven | — | `RequestApproval`, `GetApproval`, `QueryAuditLog` |
| HOST-NTF-001 · A category I switched off does not reach me | unproven | — | `Update`, `NotifyUser`, `ListNotifications` |
| HOST-NTF-002 · What I must be told reaches me anyway | unproven | — | `Update`, `NotifyUser`, `ListNotifications` |
| HOST-SRC-001 · A connected source's changes reach the module | unproven | — | `AddGitHubSource`, `SyncSource`, `ClaimJobs`, `FetchDatasourceBlob` |
| HOST-SRC-002 · A rewritten history reconciles | unproven | — | `SyncSource`, `ClaimJobs` |
| HOST-DASH-001 · A private dashboard is mine and my administrators' | unproven | — | `CreateDashboard`, `ListDashboards` |
| HOST-DASH-002 · Sharing makes it readable, not writable | unproven | — | `ShareDashboard`, `GetDashboard`, `UpdateDashboard` |
| HOST-ENT-001 · A plan gate refuses what the plan excludes | implementation gap | — | `GetOrgEntitlements` |
| HOST-ENT-002 · A flag cannot buy an entitlement | implementation gap | — | `UpsertFeatureFlag`, `GetOrgEntitlements` |
| HOST-USE-001 · Usage counts once | unproven | — | `ConsumeUsage`, `GetUsage` |
| HOST-USE-002 · A quota holds | unproven | — | `ConsumeUsage`, `GetUsage`, `OverrideEntitlement` |
| HOST-BILL-001 · The public catalogue is what is actually on sale | unproven | — | `ListPublicPlans` |
| HOST-UI-001 · A module brings its own pages | implementation gap | — | — |
| HOST-OBS-001 · Recording waits for my consent | implementation gap | — | — |
| HOST-OBS-002 · Consent for one thing is not consent for another | implementation gap | — | — |

The GitHub compiler already detects missing bases, diverged history and
truncated comparisons and emits a snapshot (#487/#501). HOST-SRC-002 remains
unproven until a named story test establishes consumer convergence and replay
against the installed composition; absence of detection is no longer its gap.

Where a row says *implementation gap*, this is what is missing:

- **HOST-ENT-001 / 002** — no "not included in the plan" refusal path exists in
  the accounts service; the gate has to be found or built, and the
  provider-neutral flag path proven unable to open a plan-excluded capability.
- **HOST-UI-001** — a remote whose chunks come from a non-manifest origin cannot
  load its stylesheet, `style-src` carries `'unsafe-inline'`, and a skin with one
  unknown key is silently swapped for the stock default. One ADR has to
  supersede the drifted ADR-0002 bullets (style isolation at the mount boundary;
  solution assets served same-origin through the host proxy), and the story
  gains an `And`: a rejected skin is never silently swapped for another.

  The skin vocabulary itself is now four layers — an ordinal type scale, the
  roles that point into it, the slots that say which role a surface uses, and
  the control rungs — with the authoring-time survival check exported from the
  contract so a descriptor-owning repository catches a mismatch before it
  deploys rather than rendering the stock default (#823).
- **HOST-OBS-001 / 002** — the browser error-tracking SDK records navigation
  breadcrumbs with the query string, so `history.replaceState` away from a
  secret-bearing URL writes the OAuth `code`/`state`, the magic-link `token` and
  the App-setup `state` into later events. The fix is in the SDK initialisation:
  one redaction list driving a `beforeBreadcrumb` and a `beforeSend`.

### Not yet on the page

Impersonation ships (`ImpersonateUser`, `StopImpersonation`, a required
justification, 48 methods refused during a window) but the page has no story
for it, so it is not a promise. Proposed to the handbook as
**HOST-ADM-001 · Support access is bounded, ended, and on the record**; it is
proven here once the page carries it. Its remaining implementation half: a
window closed by token expiry or by `RevokeSession` records no
`user_impersonation_ended`; each has to record exactly one end event.

### Contract changes the page is waiting on

- `ModuleCapabilitiesService.PlaceRecord → PlaceResource`
  (`ModulePlaceResourceRequest` / `ModulePlaceResourceResponse`): the handbook's
  agreed name for host placement. A descriptor change — the full regeneration
  chain, a buf-breaking waiver, the interface-docs gate — and the page's
  Interface block is re-pinned to the release that carries it.

## Exit criteria

- Thirty-one story tests (thirty-two with `HOST-ADM-001`) pass on `main`, none
  skipped, and `story-trace-gate.mjs check` against the current page reports
  nothing in either direction.
- The page carries a `Surface:` line on every backend story, spelled as its
  Interface block spells the operations, and the handbook pull request that adds
  them and `HOST-ADM-001` is merged.
- `PlaceResource` is the only placement operation in the contract and is in a
  tagged release the page is re-pinned to.
- One ADR supersedes the drifted ADR-0002 bullets and the frontend matches it;
  no query-string secret reaches a breadcrumb or event URL, and a test proves
  it; a descriptor with one unknown key does not render the stock default
  silently, and a test proves it.
- Expiry and `RevokeSession` each record `user_impersonation_ended` exactly once.
- No file outside `docs/historical/` is a plan: no `Last reviewed` line, no
  checkbox backlog. (`DOGFOODING.md` is a scripted walkthrough, not a backlog.)
- A `main` run reports at least one `reused` task after an unchanged rerun.

## Operator actions

Things only a person with the keys can do; nothing here proceeds past them.

- Mint the module-package provenance keypair — the first
  `module-package/v0.1.0` is blocked on nothing else.
- Merge the handbook pull request(s): `Surface:` lines, `HOST-ADM-001`, and the
  re-pin of the Interface block after the `PlaceResource` release.
- Decide, on handbook tracks 0015 and 0019, whether the host's approval record
  binds to an exact action digest for the task runtime. `HOST-APR-*` are proven
  exactly as written on the page until then; no second approval system is built
  here ahead of that decision.
- Sequence the consuming solution that still imports the retired private-custody
  client library onto the orchestration module's admission surface
  before adopting the release that removes that private listener (store
  migration 148 drops its table). The full order, and the two other consumer
  conversions an upgrade depends on, is
  [module/INTERNAL_TRANSPORT.md](../module/INTERNAL_TRANSPORT.md) § Adopting a
  release that removed the TLS listeners.
- The CI result-reuse gap is the CLI's: `codefly ci run` binds a task's cache
  identity to the digest of the *installed* agent binary, and on a fresh runner
  the pinned agents are not installed when that identity is computed, so every
  task is `ineligible` and nothing is ever published. Pin the CLI release that
  resolves the agent before binding the identity, then restore the `exit 1` in
  the workflow's "published results" step.
