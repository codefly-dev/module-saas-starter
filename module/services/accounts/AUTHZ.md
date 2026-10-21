# Authorization — scope nodes, kinds, and data boundaries

This is the prose companion to the generated [`AUTHZ_MATRIX.md`](./AUTHZ_MATRIX.md)
(the RPC → policy projection). It records the conventions the generated
artifacts cannot: what the scope tree's node **kinds** mean, and how data below
the tenant is named and bound.

## The scope tree (RFC-0001 / ADR-0002)

`scope_nodes` (migration 98) is the org's hierarchical scope registry: one
row per node, a dotted `ltree` path per org, `kind` and `label` as free text,
and — for a **placed record** — a `(resource_type, resource_id)` pair that binds
the node to a real product row. Grants (`scope_grants`) attach a role to a
subject at a node and inherit to the whole subtree; per-record shares
(`record_shares`) attach a role to a subject on a single placed record.

`kind` is stored verbatim and the enforcement path never branches on it — the
authorization decision reads only paths, grants, shares, and role permissions.
Kinds are therefore **conventions for humans and tooling**, not an enforced
enum.

## Who may place a record

The access oracles resolve a record's scope from its own node, so a record is
reachable only once it is **placed** — registered with its `(resource_type,
resource_id)`. Two callers may place one, under different bounds:

| Caller | RPC | Bound |
| ------ | --- | ----- |
| Org administrator | `PermissionService.RegisterScopeNode` | the caller's tenant; any resource type |
| Composed module | `ModuleCapabilitiesService.PlaceRecord` | its bound tenant, and only the resource types its own principal grant declares |

The module bound is the `resources` list of its module registry entry — the same
declaration that says which content this host will authorize reads of, so a
module's placement authority and its read authority cannot drift apart. A module
that declares no resources places nothing: the surface fails closed on an absent
composition rather than inventing an authority for it.

Placement is idempotent on the record — re-placing it at the same path returns
the same node, which is what the module surface's at-least-once contract needs.
Re-pointing it at a **different** path is refused. A record resolves through
exactly one node, so moving it rewrites who can reach it; that is an
authorization change, and no surface performs it today.

## Data boundary = scope node

The data boundary *below the tenant* is not a new container table; it is a scope
node. Two kinds carry a reserved meaning (constants
`business.ScopeNodeKind{Solution,Collection}`):

| Kind         | Meaning |
| ------------ | ------- |
| `solution`   | One node per solution install, a child of the org root. |
| `collection` | A data container a solution creates under its solution node — the **data boundary** that content (datasources, documents) is bound to and that grants are placed against. |

Everything else stays product-defined (`space`, `customer`, `record`, …).

Because a boundary is an ordinary scope node, "share this collection with team
B" is an ordinary `GrantScope(team B, <collection path>, role)` — the boundary
is grantable, which a free-text collection string never was.

### Datasources bind to a boundary

`datasource_sources.boundary_node_id` (migration 111) references the
`collection` node a source's pulled Entries land in, replacing the old free-text
`target_collection`. `AddSource` / `AddGitHubSource` take either an existing
`boundary_node_id` or a `collection_label` that mints a new `collection` node.
Every ingest job carries that node id as the `datasource.boundary_id` attribute;
a source with no resolvable boundary is rejected at enqueue, so nothing
downstream ever writes into an unnamed, ungrantable boundary.

Until installation identity (#474) lands there is no org-root/solution node to
parent under, so a minted `collection` node is registered as a **root**; it will
be reparented under the org's solution node when that identity exists.

`UpdateSource` changes what an already-connected source fetches — its branch and
its path and file-type filters — under the same `org_admin` tenancy and the same
organization resource binding as `AddSource`. It deliberately cannot move the
boundary: the collection a source writes into is what grants reach, so re-aiming
one in place would replace a granted collection's contents without any grant
changing. The repository and the credential are out of reach for the same
reason — one is the source's identity, the other rebinds whose authority its
syncs run under (a reconnect, through `SyncSource`).

Who *triggered* a source's sync is not a datasource field. `ListSources` and
`GetSource` project `Datasource` to any org member under no declared permission,
while `SyncSource` is org-admin only and reading its actor goes through
`QueryAuditLog`'s `audit:read` gate — so a `last_synced_by` column would route
audit-classified provenance around that gate, and would flatten the impersonation
chain the audit row keeps. The audit trail is the record; see
[ADR 0008](../../docs/adr/0008-datasource-sync-actor-provenance.md).

## Listing what a subject may see

`PermissionService.ListAccessibleScopes(subject, kind, resource_type, action)`
(`EXPOSURE_INTERNAL`) is the Zanzibar "ListObjects" companion to `CheckAccess`:
it returns the scope nodes a subject may act on, so a module can pre-filter
`boundary_id IN (…)` before loading content. It resolves through the same
grant + share union as `CheckAccess` — a node is returned exactly when
`CheckAccess` would allow the same `(resource_type, action)` on it — so the two
never disagree.

## Platform administrators read without a grant

A platform `super_admin` (a `platform_admins` row with that role) reads every
scope node — every collection and every record placed under one — of whichever
organization the request acts in, with no scope grant or record share. Any
organization, but one tenant at a time: every oracle still runs under that
tenant's `app.current_org_id`, so RLS and the queries' explicit `org_id`
predicates confine the answer to it (behaviour B2), exactly as they do for a
grant. It is a third branch beside grant and share in every layered-access read
oracle, all in
`pkg/infra/postgres_layered_access.go` and `postgres_readable_sources.go`, with
the rule itself in `pkg/infra/platform_read_authority.go`: `CheckAccess`,
`ListAccessibleScopes` / `ListMyAccessibleScopes`, the point and candidate
checks behind them, and the module-facing `ListReadableSourceCollections` and
`CheckWorkContextRecordAccess`. So the host UI, a module acting for a viewer and
the notification fan-out give one answer.

It is narrow, and fails closed on every other axis:

- **`super_admin` only.** `support` and `billing` gain nothing, and neither does
  an organization owner or administrator — a flat organization role still never
  substitutes for a collection grant.
- **`read` only**, spelled exactly; a wildcard action is not read.
- **A principal only**, never a team.
- **Never on an impersonated request**, in either direction: the impersonator's
  platform role does not reach the subject they act as, and an impersonated
  administrator's role is not the impersonator's to borrow. A Work Context
  cannot be minted while impersonating, so a module's forwarded viewer is always
  someone acting as themselves.
- **Ordinary delegated subjects read independently.** A Work Context read
  intersects the owner and every ordinary actor in its chain; an actor with no
  grant fails the intersection whatever the owner's role. The explicitly marked
  installed content-read child described below authenticates module execution
  separately and retains the human's personal visibility.
- **It replaces the grant, never the declaration.** A module that declares no
  content resource reads nothing, administrator or not.

The decision says so. `CheckAccess` reports `granted via
platform_administrator`, and each `AccessibleScope` carries `basis:
ACCESS_BASIS_PLATFORM_ADMINISTRATOR` when no grant or share reaches the node
(`ACCESS_BASIS_GRANT` whenever one does). These reads emit no audit event —
the oracles are `AUDIT_EMISSION_NONE` for grants too — so the basis on the
decision is the record. Granting or revoking the platform role itself is
audited, and bumps the authorization revision of every organization the user
belongs to, which invalidates outstanding Work Contexts and read cursors.

## Explicit installed content-read delegation

An envelope operation binding marked `delegation_mode: content_read` separates
module execution authority from a person's source visibility. The host accepts
only a human root without actor hops, authenticated independently from the
calling module. Core creates a child with that module's actual service actor.
The exact binding records both modules' installation/presence lifetime and the
envelope revision. Every content read revalidates that lifetime before using
the owner's existing collection grants, shares or platform read authority.
The module actor contributes no personal grants; it also does not remove the
human's valid visibility by being treated as a second collection reader.
The actor hop is journaled before the child is returned; an append failure
refuses issuance through the existing actor-chain journal.

StartTask may admit an unscoped foreign read carried by a current marked binding
when the human has existing content-read standing. This never admits content
writes, model inference, host permissions or an absent target declaration.
The child is terminal: renewal, further exchange, actor/source-delegated
parents and source references are refused. Optional bounded lookup uses the
same child path. Ordinary operation, proof, Model and source-delegation paths
keep their prior subject intersection and credential shape.

The host retains the explicit mode through manifest/envelope intersection.
Missing target authority is a refusal or outage, never an ordinary fallback.
Mode changes on existing bindings require the existing policy-log witness;
a host without it refuses migration and preserves its current envelope.
See [Work Contexts](../../WORK_CONTEXTS.md#installed-delegated-audience-exchange)
for the envelope field, terminal-lifetime rules and full exchange contract.

`delegation_mode: module_actor` issues the same terminal child for any invoke
scope, addressed to a module or a `solution:` audience, so the callee sees the
installed module acting for the person. The person's own grants still decide
every scope; the module's hop adds none, and its binding commits to both
installations' lifetimes.

## A read grant is standing to mint a content read

A module reading content for a viewer presents that viewer's Work Context,
carrying an **unscoped** `read` of the content's permission resource type. The
capability is not organization-wide authority over the content: every read
still resolves node by node through the oracles above (the readable-source
projection, the exact-record check), intersected over the owner and every
ordinary delegated actor. So the mint admits that one permission on the same bases those oracles
accept — an organization-level role as before, **or** a live scope grant (the
owner's or a team's), a live record share, or platform read authority on at
least one node. Without this a member granted read on one collection could see
"Read" on it and still be refused every read, because the mint demanded an
organization-wide role no collection grant confers.

Every basis is bound to a real node, because the standing admitted here has to
be standing those oracles would honour. A grant gets that bound from
`GrantScope`, which refuses a `scope_path` that is not a registered node. A
share does not — `ShareRecord` writes `resource_id` verbatim, so a share may
name a record that was never placed — so the share arm joins `scope_nodes` on
the same `(resource_type, resource_id)` correspondence the share branches of
`accessibleScopesQuery` and `ListReadableSourcesPage` already apply. A share
naming nothing authorizes no read, and must therefore confer no standing to
ask for one.

It is narrow on every other axis (`workContextPermissionAllowed`, flagged by
the adapters' `markContentReads`):

- **Declared content only.** Ordinary mint standing uses content declared by
  **the audience this capability is being minted for**, not the composition's
  union. An explicit installed content-read binding supplies the bounded foreign
  read standing described below. That audience is the one module that will ever present the capability
  back, and both module-facing reads already refuse a scope outside its own
  declaration, so the union would seal scopes no consumer can use and let one
  module's declaration decide what another module's capability may carry. A
  host with no declared content widens nothing.
- **A recheck consults no declaration at all** and admits every unscoped read.
  It is the superset of every mint on purpose: a recheck issues nothing, so it
  can admit nothing a mint did not already seal, while a recheck *narrower*
  than the mint that issued a capability reports a live capability as revoked.
  That is not hypothetical — the declaration is the module registry, which
  each replica derives on its own reconcile pass, so two hosts in one fleet
  disagree about it for up to one pass after any change. Keeping the
  registry out of the staleness verdict is what makes a change not read as a
  revocation storm.
- **`read` only, unscoped only.** Any other action, and any resource-scoped
  ask, still needs a role assignment exactly as before.
- **The owner only.** A delegated actor's authority is never widened through
  a node grant.
- **Never a host permission.** `roles` and `audit` are reserved
  (`IsHostSealedScopeResource`): the host reads those off a sealed capability
  to decide how much collection metadata to disclose, rather than authorizing
  them per node, so the per-node justification above does not cover them. A
  composition declaring one as module content fails to parse, and so fails to
  boot.
- **Live — by re-resolution, not by a revision bump.** Revoking or expiring the
  last grant withdraws the standing on the next mint and on every recheck,
  because the branch requires a live grant, share or platform role each time it
  is asked. It does **not** move the authorization revision: unlike
  `role_assignments`, `scope_grants` and `record_shares` carry only
  `bump_source_read_revision`, so the sealed `authorization_revision` a
  capability carries is unchanged by a grant being revoked. A consumer that
  re-checks by comparing that revision alone will not see the revocation;
  `CheckAuthorizationRevision` catches it because it re-resolves every
  permission before it compares revisions at all.

## A derived role, and why it is not grantable

`roles.derived_from` marks a role THIS HOST derived and owns, rather than one
anybody authored: its permissions are a solution's declared consumption for ONE
module it reaches, intersected with the role an administrator chose, and it is
granted at that module's authority-root node with
`scope_grants.derived_from_grant_id` naming the grant that caused it
(`business/derived_module_access.go`;
[../../MODULE_INSTALLATION.md](../../MODULE_INSTALLATION.md#a-grant-of-a-solution-carries-the-module-access-it-declares-it-consumes)
carries the argument).

It changes nothing in the decision above: a derived grant is an ordinary
`scope_grants` row and `CheckAccess`, `ListAccessibleScopes` and
`ListAccessibleResourceIDs` resolve it exactly as they resolve any other, so
there is no second answer to "what does this viewer reach".

What is different is that it may be conferred ONLY by the derivation. A derived
role granted at some other node, or organisation-wide, would hand a subject the
module reach of a solution they hold no installation grant on — the property the
derivation exists to deny. So `ListRoles` hides it (no picker offers it),
`GrantScope` refuses it unless the write names a cause, `AssignRole` and
`ShareRecord` refuse it, `UpdateRole` and `DeleteRole` refuse it, `CreateRole`
cannot mint one, and the role catalog importer leaves it alone.

## Deferred

Boundary-level RLS inside module stores (an `app.current_boundaries` GUC) stays
deferred (RFC-0001 resolution 2); the org remains the RLS floor.
