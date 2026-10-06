# @codefly-dev/saas-ui

Reusable SaaS-domain frontend components the portal **and** solutions import — so
there is one `<DatasourcesPanel>`, not a per-consumer copy. Components are built on
`@codefly-dev/ui` primitives, styled with the shared token classes so they render
identically in the host app and in a Module-Federation remote.

The components drive a `DatasourceClient` contract. There are two ways to bind it:

- **Gateway binding** (solution remotes) — pass
  `gateway={{ apiBase, getAccessToken, refreshAccessToken }}` and the panel self-wires:
  it builds a `@codefly-dev/saas-sdk` client over a scoped transport that stamps the host's
  bearer token on every request and, on an `Unauthenticated` response, calls
  `refreshAccessToken` and retries once — so a data call survives the short-lived token
  expiring mid-session. It also mounts its own `@tanstack/react-query` provider. A
  Module-Federation remote gets the full UI from `SolutionPageProps` alone — no ambient
  query/auth context, no re-implemented fetch.
- **Injected client** (the portal) — pass `client={…}` built with
  `datasourceClientOverTransport(transport)` over the app's own transport, and provide
  the surrounding `QueryClientProvider`. Use this when the app already owns
  auth/transport (e.g. its own token-refresh interceptors) — one adapter, shared with
  the gateway path.

## Solution remotes: the host binding

A solution remote's own backend calls, and the viewer state it keys them on,
come from the kit too, so no remote carries its own copy. They are exported
from the package root and from `@codefly-dev/saas-ui/solution`, which imports
React, Connect and the SDK's service descriptors but none of the datasource
components:

- `SolutionBinding` — the props the host injects into every solution page: the
  backend half (`solutionId`, `apiBase`, `getAccessToken`, `subscribeToken`,
  `refreshAccessToken`, `authedFetch`) plus `declaredSources`. The host's
  `SolutionPageProps` extends it, so a remote types its props against the one
  definition. `subscribeToken`
  is optional but a host that can notify should pass it: the getter stays stable
  while the token rotates underneath it, so without a subscription the kit
  re-reads on a short interval — one timer per observer, for as long as the page
  is open.
- `declaredSources: DeclaredSource[]` — what this solution declared it is built
  on (`sources:` in its registration manifest), validated by the host at
  registration and handed straight back, so the repository is stated once
  rather than once in the manifest an operator reads and again in the bundle.
  Feed an entry to `<DeclaredSourceCard>`. Optional, and absent from an older
  host: treat absence as "the host does not tell me", never as "this solution
  declares nothing".
- `solutionFetch(binding, path, init)` / `solutionJson<T>(binding, path, init)` —
  a request to the solution's own backend at `apiBase + path`, same-origin,
  through the host's `authedFetch` (refresh-then-retry on a 401) with the bearer
  stamped. `solutionJson` throws a `SolutionRequestError` carrying the status and
  the backend's own `{ "error" }` message.
- `useSolutionJson<T>(binding, path, cache?)` — one JSON resource as
  `loading | error | ready`, re-read when the viewer, base or path changes; an
  answer that arrives after the viewer changed is dropped.
- `solutionTransport(binding, path?, onUnauthorized?)` — `solutionFetch`'s
  twin for generated Connect clients: at `apiBase` it reaches the host's
  platform procedures, at `apiBase + path` what the solution's backend serves
  there (on solution-runtime-go, `/modules/<as>` is a consumed module's
  passthrough). The bearer is stamped, requests go through `authedFetch`, and a
  refused authority is reported.
- `useAccessibleScope(binding, orgId, resourceType, action)` — whether the
  viewer holds an action anywhere in the org (`loading | error | none | some`),
  from the host's accessible-scope API. It decides what a page offers; owners
  still authorize every read.
- `viewerOrganization(token)` — the org the token names: a selector for
  requests and local state, never authority.
- `viewerAdministersOrganization(token)` — whether the token names an
  organization owner or admin, or a platform super administrator: the tier the
  host requires to manage sources and grant read access. It decides what is
  offered, never what is allowed.
- `<NoReadableCollection canGrant={…} />` — what to show a viewer who may read
  no collection (`useAccessibleScope(…) === "none"`): a member is told to ask an
  organization administrator and where the grant is made; an administrator gets
  the link to make it (`COLLECTION_ACCESS_PATH`, the host's
  `/admin/datasources`). Pass `grantsOnThisPage` when the caller renders the
  grants surface itself — the notice then names that section instead of linking
  to the page the reader is already on.
- `viewerIdentity(token)`, `useAccessToken(getAccessToken)`,
  `useViewerEpoch(getAccessToken)` — who the current credential speaks for,
  stable across a refresh and changing with the viewer. They partition local UI
  state; they never authorize anything (the claims are read unverified).
- `viewerPrincipal(token)` — the principal reading (the token's `sub`). On an
  impersonation token it is still the real actor, never the user being viewed
  as. It decides what reads as "You", nothing else.

### Who did something: principal names

Every module records who did something as a principal id. The host holds the
directory that names them, so a solution asks the kit rather than listing the
directory itself:

```tsx
<PrincipalNamesProvider binding={props}>
  <Page />
</PrincipalNamesProvider>

// anywhere beneath it
<PrincipalName principal={comment.author.principal} />
const names = usePrincipalNames(ids); // Record<id, PrincipalName | undefined>
const nameOf = useNameOf();           // (id) => string | undefined, for a kit's `nameOf` prop
const directory = usePrincipalDirectory(); // loading | ready | refused | failed
```

- **One read per page.** `<PrincipalNamesProvider>` reads the viewer's
  organization directory (`DirectoryService.ListOrganizationMembers`) the first
  time anything beneath it asks, once however many cards ask, and keeps the
  answer until the viewer changes (the change `useViewerEpoch` counts). A new
  viewer's page never sees the last viewer's names, not for one render, and an
  answer that arrives after the switch is dropped. Its binding needs
  `getAccessToken`: names are kept per viewer, and a binding that cannot say who
  the viewer is cannot keep them apart.
- **Only the viewer's organization.** The host answers the directory only for
  an organization the viewer belongs to, and the kit also ignores any row naming
  another one. A principal outside it resolves to `undefined`, never to another
  tenant's member.
- **`PrincipalName = { display, email?, avatarUrl?, you }`.** `display` is the
  directory's label for the member. The host's tenant directory carries a
  member's email and no display name or avatar, so today `display` is the email
  and `avatarUrl` is never set; a richer directory changes what is shown without
  changing a consumer. A member the directory gives no label (a deleted
  account) is unknown, never `""`. `you` is an exact match with
  `viewerPrincipal`: a near match (another case, the same email) is never "You".
  `you` is set only on a listed principal, so the viewer is `undefined` from
  `usePrincipalNames` whenever the directory does not list them (while it loads,
  when it was refused, or when an administrator views as someone outside it). A
  caller deciding "You" compares against `viewerPrincipal` itself, as
  `<PrincipalName>` does.
- **States are said once.** A name is `undefined` while loading, when the read
  was refused, or when the principal is not listed. Which of those it is,
  `usePrincipalDirectory()` says once for the page:
  - `refused` is kept for the viewer. It means the host answered permission
    denied, the credential names no organization, or there is no credential at
    all (a signed-out viewer).
  - `failed` is anything else, and it stays `failed` while a retry is in flight.
    A page never goes back to `loading` once the read has settled, so a page that
    renders names only after it settles cannot remount itself into a request
    loop. When a later read happens depends on the failure:
    - unauthenticated: on the next token the host hands over;
    - a gateway or network failure: by the next component that asks after a
      backoff (1 s, doubling, at most a minute);
    - invalid argument, not found, unimplemented, or failed precondition: not
      again for this viewer, because the request itself is wrong.
- **Late answers are dropped.** An answer is stored only if the host's current
  token still names the viewer who asked, so a switch the page has not noticed
  yet (a host without `subscribeToken` is polled every 250 ms) never shows the
  previous viewer's names.
- **`<PrincipalName principal>`** renders "You" on an exact match, the
  directory's label once known, and otherwise the id shortened to its ends
  (`0000…00a1`). The full id is always its `title`, and `data-state` (`you`,
  `named`, `loading`, `unknown`, `refused`, `failed`) says which it is. An empty
  principal reads "someone", with `data-state="none"` and a title saying no
  principal is recorded.
- **`useNameOf()`** is the function form a module kit takes as a prop. It
  answers the directory's label or `undefined`, never "You" — a kit decides that
  from the viewer it is given — and never `""`.

## Datasources

- `<DatasourcesPanel gateway={{ apiBase, getAccessToken }} orgId={…} />` or
  `<DatasourcesPanel client={…} orgId={…} />` — lists an org's connected sources
  (repo · status · paths · branch · boundary · live updates · last sync) with per-row **Sync**/**Delete** and
  a **Connect GitHub** action. The last-sync cell shows whichever clock applies:
  `last_synced_at` for a provider that pulls, or `last ingest <date, time> ·
  <short commit>` for a github source, whose change sets the compiler enqueues
  (webhook delivery, periodic reconcile, or a tenant's "Sync now" forced
  reconcile) — it never sets `last_synced_at`, so "Never" is dropped rather than
  shown above live provenance. Loading/error/empty are first-class. Connect,
  Sync, Reconnect, Use GitHub App and Delete are offered only to a viewer who
  may use them (`canManage`; with a `gateway`, read from the viewer's
  credential): anyone else keeps a row's History and is told who connects
  sources and grants access.
  `renderSourceDetail={(source) => …}` renders beneath each repository name: the
  slot through which a consumer shows what the module that ingests a source's
  files knows about them (its per-source ingestion progress, say). The host
  names no such module.
  The **live updates** cell reads `liveDelivery`, the host's composed answer to
  "how does a change at the source reach this deployment": through the source's
  own repository webhook, through the deployment's single GitHub App webhook, or
  not at all — in which case it names the reconcile interval that bounds how
  stale the source may be. It is NOT `webhookConfigured`, which reports only
  whether a signing secret is stored against the source: an App-backed source
  holds none, so reading that flag showed every source on the recommended
  connect path as having no live delivery whether or not the App webhook was
  registered. An older host sends no `liveDelivery`; the cell then says so rather
  than presenting a guess as an answer.
  `renderSourceExecution={({ source, sync }) => …}` is the sibling slot for the
  **durable execution** of a sync — the fan-out task it became, its items,
  attempts, receipts and dead-letters. Opened from the row's **Execution**
  action and from the progress card; offered on no row when the prop is absent,
  since an empty panel promising an execution view is worse than not offering
  one. The host renders the frame and hands you the sync it resolved; what runs
  the work is a module this package may not name, so the view itself is yours.

  Three rules the host cannot enforce for you, each of which fails by
  *succeeding*:
  - **Pass the slot only for a viewer who may read the organization's
    executions.** The durable work behind a sync is admitted by the module that
    ingests it, not by the person reading the panel, so a caller-scoped "my own
    runs" read is a correct answer to a different question — and its answer here
    is an empty page. The panel's own empty state says "this source has never
    synced", so an under-permissioned viewer is told something false by two
    correct components.
  - **Distinguish "no runs" from "you may not see the runs"** in what you
    render, for the same reason. The kit carries that sentence:
    `<SourceExecutionRestricted />` renders "Sync runs are visible to
    organization administrators." so it reads the same wherever it appears.
    Render it when your viewer cannot read the organization's runs — it names
    no module and no permission, because which authority governs this is yours
    to know. If you render *nothing* the host says the neutral thing instead
    ("No runs to show for this sync. If you expected some, you may not have
    permission to see them."), since it cannot tell the two apart and must not
    let silence pick one.
  - **Never report a count taken from a page of results.** One sync can produce
    many runs, and the host already knows how many without paging:
    `sync.changes.snapshot ? 1 : sync.changes.files` — on the incremental path
    that *is* the hand-off count by construction, counted after the source's
    path and extension filters. Show it beside the runs you loaded, not as a
    count of them, so the two disagreeing reads as work that was never admitted
    rather than as a paging bug. It is final only once the sync is terminal:
    mid-sync it counts the hand-offs enqueued so far.
  A source whose provider does not yet meet the host's datasource connector
  envelope is badged **Non-conformant provider** with the host's stated gap:
  it keeps syncing, but the host connects no new source of that provider. The
  flag is keyed on `conformance_gap`, so an older host that sends no
  conformance fields flags nothing.
- `<ConnectGitHubForm onSubmit={…} … />` — the connect form (repo, paths, branch,
  target collection, webhook secret, and an access token only on the PAT path).
  Its Authentication choice always offers **Public repository (no token)**: the
  host connects it only once GitHub reports the repository public to an
  unauthenticated request, stores no credential, registers no webhook (the form
  hides the webhook secret), and keeps it current by periodic sync under
  GitHub's unauthenticated limit of 60 requests an hour per IP address. When the
  App is installed on that repository for the organization, the host uses the
  App instead.
- `<DeclaredSourceCard gateway={{ apiBase, getAccessToken }} orgId={…}
  declared={binding.declaredSources[0]} />` — one source a solution **declares**
  it is built on, in one of three states. A solution built on one known
  repository must not ask the person which repository: it declares it, and all
  that is left to supply is the credential.
  - **Set up** — nothing connected serves the declaration. The three credential
    modes (App / public / PAT) and nothing else; `repo`, `paths` and `ref` come
    from the declaration, are rendered as text, and are submitted unedited to
    `AddGitHubSource`. There is no repository control at all, which is the
    point: a field for it would let the person connect something other than
    what the solution reads. The entries land in a collection named by the
    declaration's `label`, falling back to the repository.
  - **Connected** — the matched source is `ACTIVE`: its last ingest and commit,
    how live updates reach it, the reconcile interval that bounds its
    staleness, **Sync now**, and the same `renderSourceDetail` slot the panel
    offers so the consumer can show the ingesting module's progress.
  - **Error** — `DEGRADED` or `PAUSED`: the host's `status_reason` verbatim,
    and Reconnect, which asks for the credential again (a replacement PAT, or
    nothing on the App and public paths) and enqueues a sync.
  There is no fourth state. A source whose status an **older host does not
  report** (the field decodes to its proto default, which the gateway maps to
  `unknown` rather than guessing) reads as **Connected** and says its state is
  not reported: it matched the declaration, so it *is* connected, and calling
  the absence of a status an error would paint a healthy source red with no
  `status_reason` to show for it and offer to Reconnect — asking a person for a
  credential again to fix nothing. Sync is still offered; Reconnect is not.
  A viewer without `canManage` still sees the state and the reason, and is told
  who acts; the host refuses the calls either way. The card takes no query or
  auth context of its own — the same `client | gateway` binding
  `DatasourcesPanel` takes — and adds **no RPC**: it reads `ListSources` for the
  org and matches on `provider` + `repo` (case-insensitively, as GitHub treats
  `owner/name`) plus `paths` when the declaration names any.
  Two connected sources matching one declaration are **reported, never silently
  picked**: they can disagree about credential, branch, scope and health, so
  rendering the first would sync one and leave the other ingesting invisibly.
  `matchDeclaredSources(declared, sources)` is that rule as a pure function, for
  a consumer rendering the states in its own shell; `declaredCollectionLabel`
  resolves where connected entries land.
- `CredentialMethodField` / `AccessTokenField` / `WebhookSecretField` /
  `AppInstallPrompt` (with `CredentialMethod`, `credentialMethodFrom`,
  `fieldErrorClass`) — the credential block `ConnectGitHubForm` and
  `<DeclaredSourceCard>` both render, presentational and controlled, exported
  so a consumer building its own shell around `matchDeclaredSources` can **ask
  for the credential without copying the form**. Compose these; do not copy
  `ConnectGitHubForm` — `solutions/README.md` refuses a copied capability
  wrapped in a shared card, and these three modes are the whole of what a
  person decides between, so two surfaces that word them differently teach two
  different products.
  Do not mount this and `<DatasourcesPanel>` on the same page: both redeem the
  GitHub App's single-use return state, and the loser reports a rejection for an
  installation that in fact succeeded.
- `createDatasourceClient({ apiBase, getAccessToken, refreshAccessToken })` — builds the
  gateway-bound `DatasourceClient` (with 401 refresh-and-retry) directly, for driving the
  hooks outside the panel. `datasourceClientOverTransport(transport)` does the same over a
  transport you already own. Its `getSourceSync(orgId, sourceId)` reads a source's latest
  sync as typed phases (queued, fetching, compiled, handed off, done, failed) with a
  timestamp for each, the compiled change set, and a typed failure.
- `onSourceSyncRequested((sourceId) => …)` — hears every sync the kit's client enqueues
  (Sync now, a reconnect, the first sync a connect starts), so a view rendering a source's
  progress in the panel's per-source slot can watch closely at once. It returns the
  unsubscribe. The registry lives on `globalThis`, so a remote's own copy of the kit hears
  the host panel.
- **Live sync progress.** While a sync runs the panel shows a card per source
  above the table: a phase bar (queued → fetching → compiled → handed off), the
  compiled change set's counts, and — when it goes quiet or fails — what is
  wrong. Every phase, count and failure sentence is the host's own projection,
  stamped from the durable sync and hand-off jobs; nothing is inferred from
  elapsed time. The read tightens to a two-second poll while a sync is active,
  slackens to thirty seconds once it settles, and is re-armed the instant
  `onSourceSyncRequested` fires, so the bar appears on the press rather than on
  the next interval. A card leaves the panel ten minutes after its sync
  finished — an older sync lives in History.
  Six states are distinguished, because collapsing any two of them misreports a
  healthy sync: **queued**, **running**, **no progress** (a phase that has not
  advanced for `DEFAULT_STALL_AFTER_MS`; still running, never called failed —
  only the host may say that), **retrying** (the host failed an attempt and will
  try again, with the reason and the wait), **done**, **no changes** (finished
  having handed nothing off — the source had not moved, which is a success and
  not an empty failure), and **failed**.
- `describeSync(sync, { now, stallAfterMs })` → `SyncProgressReport` is that
  decision as a pure function, exported so a consumer can render the same states
  in its own shell — a line in a header, say — without re-deriving them from the
  phase names and getting the terminal cases wrong. `<SourceSyncProgress source
  report />` is the card, and `useSourceSync(client, orgId, source)` the read.
- Hooks over a `DatasourceClient`: `useListSources`, `useAddGitHubSource`,
  `useSyncSource`, `useDeleteSource`, `useAccessibleScopes`.

### Connecting through the GitHub App

`Connect GitHub` offers the App as the default path and keeps a repository-scoped
fine-grained PAT as a named alternative for existing connections and development.
The panel drives `beginGitHubAppSetup`, sends the browser to the install URL the
host mints, and on the way back redeems `completeGitHubAppSetup` with the echoed
state, the installation id, and the authorization code GitHub appends when the App
requests user authorization during installation — the host trades that code to prove
the caller can reach the installation they name, so the App must be registered with
it enabled. It then lists the repositories that installation grants,
skipping the ones this organization already connects and offering each repository's
reported default branch. Connecting that way sends no access token at all. An
existing source moves onto the App in place with `migrateGitHubSourceToApp`.

These three client methods are optional. A consumer adapting its own client may
implement none of them: the App path is then hidden, the PAT path is unaffected,
and the panel leaves a redirect's parameters and the address bar alone so that
consumer can handle the return itself.

**Deployment prerequisite.** GitHub returns the browser to the **Setup URL set on
the App registration**, which nothing in this repo can set or verify. Point it at
the page that mounts this panel (`https://<host>/admin/datasources` in the portal)
and enable "Redirect on update", or the tenant installs the App and is returned no
source and no error. See `module/configurations/local/github-app.env`.

### Data boundaries

Each source binds to a collection scope node. Connecting or creating a collection
creates **no creator, default-team, or organization-wide read grant**. Accounts
is the authority for read on the declared content resource; source labels and flat administrative
roles never substitute for a collection grant. The one exception is a **platform
administrator** (the platform `super_admin` role), who reads every collection
without a grant — never while impersonating. `listAccessibleScopes` marks such a
boundary `viaPlatformAdministrator`, and the panel says the viewer reads it as a
platform administrator rather than through a grant (see accounts `AUTHZ.md`,
"Platform administrators read without a grant").

The host's `/admin/datasources` picker lists existing collections, their active
read grants (including inherited grants), and the creator's current read access.
Administrators can choose **any number of** members and teams and grant a role
containing only that resource in one press, or revoke a displayed grant. The
grants are applied one at a time on purpose — the host mints the read role on
its first use and concurrent grants would race to create one name, which is
unique per organization — and the outcome is reported per subject, since a run
that granted six of eight is neither a success nor a failure; the ones that did
not land stay selected, so the retry is the same press. Revoking an inherited
grant removes that role at its ancestor and all descendants; the confirmation
names this impact.

**`CollectionGrants` is also offered at connect time.** `DatasourcesPanel`
opens it on the collection a connect just landed in, passing `connectedRepo` so
it names what was connected and that nobody can read it yet, and `onDismiss` so
it can be put away — the grant belongs where the person already is and knows who
needs it, not in a separate later journey. "Manage read grants" wins when both
are open, since that was asked for afterwards.
Accounts checks admin authority and emits its transactional grant/revoke audit and
lifecycle events. The host audit page resolves actor names, and grant rows display
the granting actor. The host adapter supplies `listCollections`,
`listGrantSubjects`, `grantCollectionRead`, and `revokeCollectionRead` to the
transport-free `CollectionGrants` component. Gateway-bound source panels link to
that host action; admin permission APIs are not added to the public SDK.

`createDatasourceClient` queries the SDK's caller-scoped `ListMyAccessibleScopes`
with read on that resource, follows every page, and propagates failures. No readable
collection, permission-service failure, and an unresolved lookup have separate
states; none is an empty search result or proof of an indexing failure.

A consuming collection or chat page can wrap **all** private state beneath
`CollectionReadBoundary` (inside its React Query provider), passing `client`,
`orgId`, and the collection's `nodeId`. It unmounts its children on denied or
failed permission refreshes. Permission queries refresh every five seconds,
including in background tabs, and on focus; local grant mutations invalidate
them immediately in the same query client. Server-side document/search/stream
requests must still enforce current Accounts grants. Browser timers can be
throttled, so this polling is not an instantaneous revocation guarantee.

The consuming page owns cancellation and deletion of any private query caches,
stream buffers, persisted history, or state outside the boundary. Dispose those
on unmount and reauthorize before restoring content. Do not keep private content
in an ancestor of the boundary. Render "No indexed content" only inside an
allowed boundary after a successful empty content response. This repository has
no consuming collection or chat screen; its wrapper tests exercise state disposal,
while end-to-end ingestion and those screens require verification in a consuming
solution.

The collection panel reads its content resource from the `contentResource` on the
gateway binding, and the server matches grants against the `resources` each composed
module declares in `MODULE_PRINCIPALS`. Neither names a resource itself: this kit
ships with the host, which holds no domain content. A deployment that declares none
grants nothing, and its `listAccessibleScopes` rejects rather than resolving
empty, so the panel reports the viewer's read access as unresolved. It does not
report the viewer as refused: an empty scope list is a verdict about their
authority, and nothing authorised the kit to state one. The method stays present
on the client either way, so a consumer calling it through the optional-property
`!` keeps a resolvable call rather than a missing one. `ListCollectionAccess`, which the host answers from
the union of every composed module's declared resources, still lists a collection's
readers — so an administrator can see a grant that this deployment's own
`contentResource` does not cover.

The `dev-admin` fixture provisions an `Example Collection` and a `Collection reader`
role via Accounts' registration/grant service paths. Only `admin@acme.com` and
`bob@acme.com` receive that grant. Select this existing collection when connecting
a demo source; other fixture viewers remain ungranted. Reseeding reconverges the
declared grants, so test revocation without restarting the fixture runtime.

## Audit

`<LastLogin at subject? label? />` says when someone last signed in the way a
person reads it ("Last login · 12 seconds ago"), and keeps it true as time
passes: it re-says itself every second for the first minute, then every 30
seconds, then every five minutes. The exact time is on the `<time>` element
(`dateTime`, and a `title` for hover) and on the line beneath. `at` is an ISO
string or a `Date`; `null`, or anything that is not a date, reads "Never" with
"No sign-in is recorded yet", never "Invalid Date". A solution hands it the
value it read (from the audit log, say) and owns nothing of how it is said.
`relativeTime(at, now, locale?)` is its words alone, counting whole units
elapsed through `Intl.RelativeTimeFormat`, for a consumer's own shell.

## Installing from a solution

This package is published to GitHub Packages under the org's `@codefly-dev`
scope, so a consumer needs that scope routed to the GitHub registry with a read
token:

```
@codefly-dev:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

Everything this package renders with is a **peer**, not a bundled dependency, so
that the host and every Module-Federation remote share one instance of each (a
second copy of React Query or React Hook Form means a second context, which is
the split the sealing invariant exists to prevent). That means the consumer
installs them:

```
npm i @codefly-dev/saas-ui @codefly-dev/saas-sdk @codefly-dev/ui \
      react react-hook-form @hookform/resolvers zod \
      @tanstack/react-query @connectrpc/connect @connectrpc/connect-web @bufbuild/protobuf
```

Omitting one does not fail the install — npm only warns about unmet peers — it
fails later at import. (`peer-docs.test.ts` pins this list to `peerDependencies`,
so it cannot drift the way a hand-maintained list otherwise would.) `@codefly-dev/saas-sdk` is deliberately a range rather
than an exact pin: it versions independently of this package, so an exact pin
here would make an SDK patch bump uninstallable against the published saas-ui.

## Styling

Components are styled with Tailwind utility classes against the shared shadcn
design tokens (`bg-primary`, `text-muted-foreground`, `border-input`, …), same as
`@codefly-dev/ui`. They ship no compiled CSS, so **the consuming app's Tailwind must
scan this package's source** or the utilities used only here (e.g. the modal's
`bg-black/50` overlay) won't be generated and the components render unstyled.

- In this monorepo the portal gets this for free: Tailwind v4 automatic content
  detection already scans `packages/**`.
- An external consumer that installs the built package from `node_modules` (which
  Tailwind v4 excludes by default) must opt it in, e.g.
  `@source "../node_modules/@codefly-dev/saas-ui/dist";` in its CSS.

GitHub connection forms accept `fileExtensions` (for example `.md, .mdx`) and
offer a Markdown preset. The host intersects suffixes with paths for snapshots
and incremental changes; empty keeps all file types. The filter is selected at
connection time and does not rewrite existing sources.

The SDK peer floor is **0.3.14**: principal-name resolution imports the directory
service introduced in that release. Earlier SDK versions must be rejected during
installation rather than accepted and then failing when the component loads.
