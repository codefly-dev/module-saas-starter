# Notifications

The host owns delivery and the inbox lifecycle. A module calls `NotifyUser` as
its registered backend principal; the host checks the declared tenant and the
recipient's membership. The response's `delivered` flag means an **in-app row**
was written, not that an email, Slack message, or text message was sent.

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
