# Notifications

The host owns delivery and the inbox lifecycle. A module calls `NotifyUser` as
its registered backend principal; the host checks the declared tenant and the
recipient's membership. The response's `delivered` flag means an **in-app row**
was written, not that an email, Slack message, or text message was sent.

A module-supplied `action_url` must be a same-origin relative path: one leading
slash, no authority, no scheme, no backslash, no ASCII control character, no `.`
or `..` segment. Both the string as given and its decoded path are judged, so a
value such as `/%2F%2Fhost` — which a browser keeps on the origin but `url.Parse`
hands a caller as `///host` — is refused too. `NotifyUser` and `NotifyOrgAdmins`
answer `InvalidArgument` for anything else, before the membership read and before
any row is written, so a module cannot put an off-site link in a member's inbox.
One refused destination fails the whole `NotifyOrgAdmins` call: no administrator
is notified, rather than all of them receiving a notice they cannot follow.

The same rule is applied again when a destination is followed.
`ResolveNotificationAction` reports an unusable stored `action_url` exactly as it
reports an absent one — `notification not found` — because the write gate cannot
reach a row already in the table, and `NotifyOrgAdmins` accepted off-site links
before the gate existed. The product's own router narrows the answer to a
same-origin path once more before navigating.

A recipient who is not a member answers `FAILED_PRECONDITION` with the
`google.rpc.ErrorInfo` reason `TENANT_MEMBERSHIP_MISSING`, separate from the way
the calling module's own failures are refused. A failure of the caller's
**authority** — an identity it cannot prove, a principal the host does not
admit, a tenant it is not bound to, a queue or namespace its grant does not
cover — answers `UNAUTHENTICATED` or `PERMISSION_DENIED`; a malformed request
answers `INVALID_ARGUMENT` whatever the caller's authority; and an operational
condition of the host answers `FAILED_PRECONDITION` under its own reason. So a
sender driving the call from a journal can drop the notice for a departed person
without dropping it for a module the host does not admit as a principal.
**Key on that reason, never on the code:** `NotifyUser` and `NotifyOrgAdmins`
also answer `FAILED_PRECONDITION` with reason
`NOTIFICATION_IDEMPOTENCY_CONFLICT` when an idempotency key was already used
for different content, which is the sender's own bug and must not be dropped as
a departed recipient. A host that cannot re-read live module authority answers
`UNAVAILABLE` with reason `LIVE_AUTHORITY_UNAVAILABLE`: an operator's to repair,
and work a sender must retain. A module context this host does not admit (not
registered, installed nowhere, tenant moved, another registry binding) answers
`UNAUTHENTICATED`: the module drops its context and mints again. An absent or unrecognised reason is retainable too — only
`TENANT_MEMBERSHIP_MISSING` is final.

**The consuming side of this distinction is not done, and this host cannot
finish it.** The host now answers these facts separably; a sender only benefits
once it reads them. At the time of writing the Go SDK facade wraps the error
without exporting a membership sentinel, and the consuming notification adapter
classifies `PermissionDenied` and *every* `FAILED_PRECONDITION` as terminal — so
an unregistered principal, a rotated secret and an unconfigured host still let a
journal cursor advance past notifications nobody received, which correcting the
configuration afterwards does not retry. That defect lives in those
repositories, predates this contract, and is theirs to fix: the sentinel keyed on
code, domain and reason, then the adapter repinned to it. Until that lands, read
this section as describing what the host guarantees, not what any consumer
currently does.

The host shell renders the bell, SSE unread updates, inbox and banner. The
banner requests unread items in the active organization before pagination and
continues through pages removed by resource-visibility checks. Dismissing it
marks that notification read. Following an action rechecks current access via
`ResolveNotificationAction` before navigation. The reusable visual component is
`Banner` from `@codefly-dev/ui/layout`; it owns no fetching or delivery.

Inbox cursors order by `(created_at, id)` so notifications written in one
transaction remain reachable. The server accepts old timestamp-only cursors
during rollout. `ListNotifications` accepts optional `org_id` and `unread_only`
filters; neither changes the authenticated recipient. Resource visibility is
still evaluated at read time. The inbox has controls for older and newer pages.

Optional product, marketing and digest notices honor preferences. Security and
billing notices bypass optional-category suppression. A consuming module must
choose the category from the notice's meaning, never as a way to bypass opt-out.

## Datasource updates

A manual sync carries its authenticated requester into the existing ingestion
seam (#842). The content-owning consumer emits the completion notification only
when its ingestion commits. A successful source fetch or enqueue is not proof
that content is ready. The host banner (#843) presents the resulting in-app row.

GitHub sources can select file suffixes at connection time (`file_extensions`),
intersected with path prefixes. Values are normalized, validated and deduplicated;
empty keeps the previous all-types behavior. Snapshot and incremental paths use
the same selection. A rename into the selection adds, a rename out removes, and
a rename inside preserves the rename. Snapshot payloads and delivery attributes
both carry the host-resolved `refs/heads/...` binding, including a repository's
non-default-name default branch. Existing sources and content are not rewritten.

## External transports and delivery status

`accounts/pkg/notificationdelivery` contains a single-attempt Slack bot API
transport, raw-body Slack signature verification, and a provider-neutral sender
interface with SMS number-shape validation. These are **not wired into
NotifyUser**. No customer Slack installation or live SMS send is enabled by them.
The internal platform-operations webhook is unchanged.

Slack output uses plain-text blocks with mention parsing and unfurls disabled.
The transport rejects redirects, bounds response reads, redacts provider failures
and distinguishes rate limits from ambiguous acceptance. An HTTP timeout after
sending may mean Slack accepted a message: the adapter never retries it or
promises exactly-once network delivery. Signature verification enforces the
five-minute window but is not an event-ID deduplication store or tenant mapping.

SMS has no provider and fails with `ErrNotConfigured`. E.164 syntax establishes
neither ownership nor consent; it must not activate a settings toggle. Selecting
and provisioning a provider, verified-number enrollment and consent precede any
real text delivery.

The existing integration track is #838. Authenticated tenant/workspace installs,
encrypted token lifecycle, user-to-Slack recipient binding, preferences, durable
inbound event acceptance and Runtime-driven delivery remain outstanding there.
The host queue retirement is #835: new notification delivery must use the journal
and internal idempotent operations consumed by Runtime, not new host jobs,
retry tables or a private receipt ledger. SDK-Go #32 is still unmerged and #31
still owns the generic receipt lookup service. Until that recovery path is
available and composed, external notification delivery is not declared shipped.
Email also remains limited to the host's existing transactional flows.
