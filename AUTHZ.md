# Authorization model — three layers, defense-in-depth

> The starter ships three orthogonal authorization checks. Each is
> sufficient on its own to block the obvious attacks; **all three
> together** mean a single bug at any layer can't leak data across
> tenants. Adopting RLS as the third layer was the missing piece.

```
                     ┌─ User-issued request
                     ▼
        ┌────────────────────────┐
        │   L1  Policy gates     │   "Should this caller be ALLOWED to
        │   (handler-level)      │    invoke this RPC, given identity?"
        └────────────────────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │   L2  Permissions      │   "Does this caller have the right
        │   (RBAC, business)     │    capability for this resource:action?"
        └────────────────────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │   L3  RLS              │   "Even if everything above said yes,
        │   (DB row-level)       │    does the row physically belong to
        │                        │    the caller's tenant?"
        └────────────────────────┘
                     │
                     ▼
                 Postgres
```

## Request identity — real actor vs effective subject

Every layer below asks its question about *a* principal, so there is exactly one
typed answer to "which principal": `auth.RequestIdentity`
(`pkg/auth/request_identity.go`). It carries two ids, because authorization and
accountability are different questions:

| Field | Answers | Used by |
|---|---|---|
| `EffectiveSubject` | "whose authority does this request run under?" | tenant membership, scoped roles, resource authority, the user-scoped RLS context (`app.current_user_id`) |
| `RealActor` | "who is this request attributable to?" | audit (`impersonated_by`), and the platform-authority gates, which withhold platform authority whenever the two differ |

For an ordinary session the two are the same id and nothing needs a special
case. They diverge only under **impersonation**: `PlatformAdminService.Impersonate`
mints a token whose `sub` is the admin and whose `acting` claim is the target, so
a support engineer can act inside a tenant **without joining it** — the
membership lookup resolves for the target, not for them.

`Delegation` is a third, separate field. The RFC 8693 `act` chain names a service
acting *on behalf of* the subject and can nest; impersonation names one user an
admin is *viewing as* and cannot. Neither is an authorization grant on its own.

### One projection, every transport

The identity is installed only by an authentication interceptor that either
verified a token locally or verified the gateway credential on a forwarded
request — never from caller-controlled headers, which are stripped
(`forwardedIdentityHeaders`) when that credential is absent.

| Path | Source of the pair |
|---|---|
| Direct Connect / gRPC bearer | `Identity.UserID` + `Identity.ActingAsUserID` from the verified JWT |
| Gateway → Connect | `X-User-Id` + `X-Acting-As-User-Id` |
| Gateway → gRPC | `x-user-id` + `x-acting-as-user-id` |
| Gateway → REST | the same headers, carried across the grpc-gateway transcoding hop by `restIdentityHeaderMatcher`, then projected by the Connect interceptor |
| Billing HTTP extensions | the same two paths as Connect |

All of them converge on `stampRequestIdentity`, which makes the **effective
subject** the wool principal and the verified database scope. `requireAuth` and
`callerID` therefore return the effective subject, and `buildAuditEntry` reads
the real actor off the same typed identity. A forwarded acting-as value that
does not parse is refused (`PermissionDenied`) rather than admitted as "not
impersonating", which would silently run the request with the admin's own
authority.

### What impersonation deliberately does not grant

- **No platform authority, in either direction.** `platformRole` (adapters) and
  `Service.requirePlatformRole` (business) both resolve to nothing while a
  request is impersonated. The target's platform grants are not the admin's to
  borrow, and the admin's own grants do not follow them into someone else's
  session — including the `super_admin` bypass inside `requireOrgAdmin`,
  `requireBillingAdmin` and `requireTeamAdmin`.
- **No nesting.** `Impersonate` sits behind that same platform gate, so an
  impersonated session cannot mint a further impersonation token.
- **No inactive target.** The target must be an active account, so a support
  session cannot outlive the account's own lifecycle.
- **A short, separately capped lifetime.** `Config.ImpersonationTokenTTL` caps
  the access token independently of the ordinary TTL, and the token carries no
  refresh half — exiting impersonation means falling back to the admin's own
  session, which impersonation never touched.

## Layer 1 — Policy gates (handler-level)

**Where:** `pkg/adapters/auth.go`, `pkg/adapters/connect_auth_interceptor.go`,
called at the top of every Connect/gRPC handler.

**What it asserts:** the caller's identity satisfies preconditions for
the RPC to even run. None of these checks know about specific rows;
they're per-RPC gates on the caller's claims + role.

| Helper | Asserts |
|---|---|
| `requireAuth(ctx)` | A valid bearer JWT or API-key was presented. |
| `requireOrgMember(ctx, actor, orgID)` | Caller is a member of `orgID` (any role). |
| `requireOrgAdmin(ctx, actor, orgID)` | Caller is admin/owner of `orgID`. |
| `requirePlatformAdmin(ctx, actor)` | Caller has the platform `super_admin` role. |
| `requireMFA(ctx, actor)` | Caller's JWT carries `mfa: true`. Used for sensitive ops (rotate webhook secret, override entitlement, GDPR delete). |
| `requireScope(ctx, "res:action")` | API-key caller has the required scope (or wildcard). JWT callers pass through — RBAC handles them. |
| `rateLimitInterceptor` | Per-key request budget. |

**What this catches:** non-members hitting a tenant's RPCs, bare JWTs
hitting platform-only endpoints, API keys without the right scope, MFA
bypass on sensitive ops, IP/key floods.

**What this does NOT catch:** logic bugs *inside* a handler that look
up the wrong row (e.g., `getWebhook(id)` without filtering by orgID).
That's L2 + L3.

## Layer 2 — Permissions (RBAC, business layer)

**Where:** `pkg/business/service.go:CheckPermission` + the
`role_assignments` / `role_permissions` / `roles` tables in migration
4. Called when a handler needs to know "can THIS subject do THIS
action on THIS resource". Backed by `pkg/infra/postgres_permissions.go:CheckPermission`.

**What it asserts:** the caller has been granted a `(resource, action)`
permission in this org, either directly or via team inheritance, with
optional fine-grained `scope` (e.g. `projects/foo`).

Wildcard semantics: `resource="*"` matches all resources, `action="*"`
matches all actions, `*:*` is the root role. Team membership inherits
to the user. Scope NULL means "global" within the org.

**What this catches:** an org member trying to do something only
admins are supposed to do (e.g. an analyst hitting `users:write`),
even though L1 said "you're a member, come on in".

**What this does NOT catch:** a bug that reads `WHERE org_id = $1`
where `$1` came from the URL not the JWT (cross-tenant leak via
mass-assignment). Or a missing WHERE clause altogether. That's L3.

### Scope semantics

`role_assignments.scope` is a fine-grained authorization dimension *within*
an org (a module, product area, project — e.g. "analyst on module-a but not
module-b"). `CheckPermission` treats it strictly in both directions: a grant
scoped to `module-a` never widens to satisfy a check that asked for the
permission unscoped. A NULL-scope assignment is deliberately org-wide and
subsumes all scopes.

| Grant scope ↓ / Check scope → | `""` (unscoped) | `module-a` | `module-b` |
|---|---|---|---|
| `NULL` (org-wide) | ✅ | ✅ | ✅ |
| `module-a` | ❌ | ✅ | ❌ |

The two edges to note: a scoped grant does **not** satisfy an unscoped check
(a narrow grant stays narrow), and a NULL-scope grant satisfies every check
(org-wide subsumes scoped). If per-role subsumption is ever unwanted, that's
a follow-up design, not the default. Team-inherited assignments follow the
same matrix.

## Layer 3 — RLS (DB row-level)

**Where:** Postgres `ROW LEVEL SECURITY` + policies on every per-tenant
table. The api wraps every per-tenant request in a transaction that
sets `app.current_org_id`. Workers that legitimately span tenants set
`app.bypass = '1'` instead.

**What it asserts:** even if the SQL hitting Postgres has no WHERE
clause at all, it physically cannot return rows belonging to a
different tenant.

```sql
-- Example policy (applies to every per-tenant table):
CREATE POLICY tenant_isolation ON foo
USING (
    org_id::text = current_setting('app.current_org_id', true)
    OR current_setting('app.bypass', true) = '1'
);
```

The `, true` second arg to `current_setting` returns "" (not error)
when the setting is missing. So:

| Setting | Effect on a per-tenant query |
|---|---|
| `app.current_org_id = '<uuid>'` | Rows where `org_id = uuid` are visible. |
| `app.bypass = '1'` | All rows visible (workers + platform admin). |
| Neither set | **Zero rows** — fail-closed. |

**What this catches:**
- A SQL injection that bypassed `WHERE org_id = $1` parsing.
- A bug that uses the wrong orgID variable (`getWebhook(id, attackerOrg)` reading another tenant's row).
- A future Store method that legitimately compiles + tests but forgot the org filter.
- A connection that gets reused across requests without resetting state — the SET LOCAL is per-tx, gone on commit/rollback.

**What this does NOT catch:** it's defense-in-depth, not a primary
gate. L1 should still reject the request before L3 sees it; L3 is the
"belt" to L1's "suspenders."

## How the three layers compose

A request to `webhookConnectHandler.DeleteSubscription(orgID, subID)`:

| Layer | Check |
|---|---|
| Auth interceptor | JWT validated → caller `userID` on ctx |
| L1 Policy gates | `requireOrgAdmin(actor, orgID)` — member with admin role |
| L1 Policy gates | `requireScope("webhooks:write")` — for API-key callers |
| L1 Policy gates | (`requireMFA` for rotate-secret only) |
| L2 Permissions | `CheckPermission(actor, "webhooks", "write", orgID)` *if RBAC is more granular than the org-admin check* |
| L3 RLS | `WithOrgTx(ctx, orgID, …)` → DELETE WHERE id = subID. RLS lets it through only if the row's org_id matches. |
| Audit emit | `saas.webhook.deleted` written to audit_events **in the delete's own transaction** — a failed audit write aborts the delete |

If any single layer is wrong, the others still hold:

| Bug | Caught by |
|---|---|
| Anonymous request | L1 (auth interceptor) |
| Wrong-org caller (hand-crafted request) | L1 + L3 |
| Authenticated org member without admin role | L1 (`requireOrgAdmin`) |
| API key without `webhooks:write` scope | L1 (`requireScope`) |
| Race between role-revoke and request | L1 cache invalidation; L3 always re-checks |
| SQL injection that drops `WHERE org_id` | L3 |
| New Store method developer forgets `WHERE org_id` | L3 |
| Cross-tenant lookup via swapped variable | L3 |

## Audit durability: what a recorded event does and does not prove

Every registered audit event declares a **durability** alongside its category
(`module/services/accounts/code/pkg/business/audit_registry.go`):

- **transactional** — the event records a privileged write (a membership, role,
  scope or share change; an API key, MFA factor, principal, installation,
  webhook configuration or credential mint; a platform-admin action). Its audit
  row and its webhook fan-out are written on the transaction the caller's
  success depends on, so the record and the change commit together and a failed
  audit write fails the operation.
- **observational** — the event records something no domain transaction owns: an
  authentication outcome, a read, or an outcome produced by an external provider.
  It is written on the emitter's own transaction precisely so it survives a
  rolled-back domain write.

Two consequences are worth stating plainly, because they are easy to assume the
other way round:

- A method policy's `emits_audit` descriptor (and the "Audit" column of the RPC
  matrix) is a **declaration of intent** from the policy, not evidence that the
  event was committed durably with the mutation. The durability classification
  and the gate below are what carry that.
- A tenant-scoped transaction is the only scope that can enqueue tenant outbox
  work (migration 72/75), so a security mutation whose fan-out must reach an
  org's endpoints runs in that org's transaction rather than under the control
  plane.

`TestAuditDurability_EmitSitesMatchTheirClassification` reads this package's own
source and fails the build when a transactional event is emitted through the
fire-and-forget path, or an observational one through the transactional path.
`TestAuditDurability_NoBypassOfTheClassifiedEmitPath` fails it when an audit row
is written without going through either, by calling the store directly. Sites a
classification cannot reach are listed, with a reason, in `auditEmitExemptions`
— a named hole, not a waiver.

What the pair does **not** do is detect a privileged write that records nothing
at all; that needs an inventory of privileged writes, which does not exist. They
catch an event on the wrong path, and a write that skips the paths entirely.

The worker paths (audit-exporter goroutine, webhook dispatcher,
billing reconciler, migration runner, platform-admin endpoints) cross scopes
deliberately, through `WithControlPlane`. There is no application-settable
bypass: the historical `app.bypass = '1'` setting is gone, and crossing scopes
now means assuming a *different database role* (`app_control_plane`, `SET LOCAL
ROLE`) with its own explicit grants, which a request transaction has no way to
reach. Each entry is recorded (`recordControlPlane`), so the crossings are
countable rather than merely greppable.

## Removing an organization member: deleted vs. rendered ineffective

Organization membership is the root of a member's authority in a tenant, and
several relations hang off it. `RemoveOrgMember`
(`module/services/accounts/code/pkg/business/organizations.go`) deletes exactly
one of them and deliberately leaves the rest alone. The distinction is whether
the relation can still produce an **allow** decision once the
`organization_members` row is gone:

| Relation | On removal | Why |
| --- | --- | --- |
| `team_members` | **Deleted**, in the same transaction | Permission resolution matches team-subject role assignments through `subject_id IN (SELECT team_id FROM team_members WHERE user_id = ...)`. A surviving row keeps producing allow decisions on its own, so it is retained authority, not residue. |
| `role_assignments` (org-scoped, user subject) | Retained | `requireOrgPermission` checks `requireOrgMember` **before** `CheckPermission`, so an assignment held by a nonmember can never be reached. Deleting it would also destroy the intent an operator expressed, which a rejoin should not have to reconstruct. |
| `installations` (owner of record, co-owners) | Retained | `firstEligibleOwner` / `isCurrentOrgAdmin` (`pkg/infra/postgres_installations.go`) derive owner eligibility from live `organization_members` on every read. A departed owner fails the installation closed and hands it to a co-owner or to nobody; the row is the accountability record, and erasing it would erase who was responsible. |
| Sessions and cached membership | Invalidated | Migration 78's row-level triggers bump the authorization revision for every deleted `organization_members` and `team_members` row, which invalidates the member's sessions. The org-membership cache entry is dropped after commit. |

Two properties make the retentions safe to state rather than merely assume, and
both are tested: the team-admin helper denies a verified nonmember before it
ever reads a team row, and a removed-then-reinvited member returns with no team
privileges because the rows were deleted, not deactivated.

The dependent delete is one set-based statement on the removal's own
transaction, so a failure aborts the removal rather than leaving a partially
unwound member behind. It is deliberately row-level (`DELETE ... USING teams`,
not a statement-level path) so the revision-bump trigger still fires per removed
membership.

### Diagnosing and repairing historical orphans

Rows written before the cleanup became transactional can still exist. They are
inert against the helper above, but they are visible in team rosters and they
keep matching team-subject role assignments for any caller that resolves
permissions without a membership precondition, so they are worth clearing
deliberately rather than sweeping up on the next removal.

Diagnose first — this is read-only and safe to run on a live database:

```sql
SELECT t.org_id, tm.team_id, tm.user_id, tm.role, tm.joined_at
FROM team_members tm
JOIN teams t ON t.id = tm.team_id
LEFT JOIN organization_members om
       ON om.org_id = t.org_id AND om.user_id = tm.user_id
WHERE om.user_id IS NULL
ORDER BY t.org_id, tm.joined_at;
```

Repair is the same predicate as a `DELETE`, run per organization after the
diagnostic output has been reviewed:

```sql
DELETE FROM team_members tm
USING teams t
WHERE tm.team_id = t.id
  AND t.org_id = :org_id
  AND NOT EXISTS (
      SELECT 1 FROM organization_members om
      WHERE om.org_id = t.org_id AND om.user_id = tm.user_id
  );
```

Run it as the schema owner (both relations are RLS-protected and the repair is
cross-tenant by nature), one organization at a time, so the row-level revision
trigger fires per deleted membership and the affected users' sessions are
invalidated. Do not fold this into application startup or into the removal path:
a background sweep that deletes authority rows on its own is a larger hazard
than the orphans it collects.

## Administrative continuity, and what `organizations.owner_id` is not

An organization that has an administrative membership keeps one. That is the
whole invariant, and it is one rule rather than one rule per verb: removing the
last owner/admin and demoting the last owner/admin are the same violation and
return the same error, `business.ErrOrgAdminContinuity`.

`business.OrgAdminContinuity` states it once, over a roster and a proposed
change (`""` projects a removal). Every writer of `organization_members`
evaluates it:

| Entry point | Where | Can change administrative eligibility |
| --- | --- | --- |
| `Service.AddOrgMember` | `pkg/business/organizations.go` | Yes — it is an upsert. `ON CONFLICT DO UPDATE SET role` makes it the demotion path, and its seat check deliberately admits an update on a full organization, so nothing else was stopping it. |
| `Service.RemoveOrgMember` | `pkg/business/organizations.go` | Yes |
| `Service.ConvergeFixtureOrgMember` | `pkg/business/organizations.go` | Yes. Seeding only adds, so a well-formed fixture never sees the rule; one that would demote an organization's last administrator fails the boot rather than converging an organization nobody can administer. |
| `Service.AcceptInvitation` | `pkg/business/invitations.go` | Yes — the invitation's role is authoritative and overwrites a membership the accepting user already holds. |
| `Resolver.acceptInvitation` | `pkg/auth/pg/resolver.go` | Yes, and it is the one outside the service layer: a raw membership upsert at *authentication* time. An invariant enforced only in `pkg/business` would not exist here. It reads `FOR UPDATE` — see below. |
| Whole-organization deletion | none in the application | No path in the service deletes an organization. A `DELETE FROM organizations` removes the memberships by `ON DELETE CASCADE` without passing through any of the above, so the invariant does not stand in the way of deleting an organization — it constrains membership mutation on a live one. |

### Eligible means the identity can still act

An administrator who cannot authenticate administers nothing. `findIdentity`
admits only `status = 'active'` and returns `ErrAccountInactive` otherwise, and
`DeleteUser` is a *soft* delete that leaves the `organization_members` row
standing. Counting every administrative row would therefore let the last usable
administrator be removed while the invariant reported the organization healthy,
so eligibility is `role IN ('owner','admin') AND users.status = 'active'`.

Request traffic cannot read a co-member's `users` row (migration 69), so tenant
code resolves that through `organization_eligible_administrators`, a
`SECURITY DEFINER` function owned by `app_control_plane` and scoped to the
caller's own organization (migration 133). This is not a convenience: a direct
join under `app_tenant` returns **zero** rows, and zero administrators reads as
"this organization never had one", which the rule exempts — the failure mode
silently disables the invariant instead of tightening it. Control-plane
transactions set no tenant org and hold `BYPASSRLS`, so they evaluate the same
predicate directly.

### A change that keeps the target administrative skips the lock

`OrgAdminContinuity` can only reject when the target ends up non-administrative,
so a promotion or an administrator-role invitation is settled from the argument
alone and never touches the organization-wide lock. That is a property of the
input, not of concurrently mutable state, which is what makes it safe: not
holding the lock can only make a concurrent check miss the new administrator and
be *more* conservative, never less. The corollary is that a demotion or removal
in a busy organization does serialize org-wide — that is the cost of the
invariant being an organization-wide fact.

An organization that has **no** eligible administrator to begin with is exempt. The rule
refuses to remove the last administrator; it does not refuse to operate on an
organization that already has none. Enforcing the stronger form would make
historical zero-administrator rows unrepairable — including by an operator
adding an administrator back. Those rows are reported, not invented: see the
membership integrity diagnostic rather than granting anyone authority from a
migration.

### The lock is the load-bearing part

Reading the roster inside a transaction does not serialize it. Two transactions
in `READ COMMITTED` — what `WithOrgTx` opens — can each observe the same two
administrators and each remove one, and both commit. The guard has to run under
a lock that both contenders take, on a key that both contenders name.

`Store.LockOrgAdministration(ctx, orgID)` is that key
(`pg_advisory_xact_lock` over `administration:<org>`). It is deliberately
coarser than `LockOrgMembership(ctx, orgID, userID)`: the invariant is a
property of the organization, so a per-member key puts the two transactions
that violate it on *different* keys and serializes nothing. The per-member lock
still exists and still does its own job — excluding a concurrent team write for
the same pair — so a path may hold both.

**Lock order, for anything that takes more than one:**

```
LockOrgAdministration   ("administration:<org>")
  -> LockOrgMembership  ("membership:<org>:<user>")
    -> LockEntitlementQuota  ("cardinality:<org>:<feature>")
```

`AddOrgMember` holds the first and third; `RemoveOrgMember` holds the first and
second. All three are `pg_advisory_xact_lock(hashtextextended(...))` in
PostgreSQL's single-`bigint` advisory space, distinct from the `(int, int)`
space scope-node registration uses. Take them in that order and there is no
cycle to deadlock on.

The pre-authentication resolver takes the same `administration:<org>` key
directly on its own transaction, so it serializes against the service layer
rather than only against itself — but the lock alone is **not** sufficient
there, and this is the subtle part.

That transaction is `SERIALIZABLE`, and its snapshot is fixed by the invitation
lookup *before* the advisory lock is taken. Acquiring a lock does not refresh a
snapshot. So a plain read, having waited politely for the lock, still reports
the administrators as of before the contender that has since committed — and
admits exactly the demotion the guard exists to refuse. SSI does not cover it
either: PostgreSQL tracks rw-conflicts only between `SERIALIZABLE`
transactions, and the service layer's writers are `READ COMMITTED`, so no
serialization failure is ever raised.

The resolver therefore reads its administrators `FOR UPDATE`. A concurrently
removed or demoted administrator then raises `serialization_failure` on the
locked row, which `WithAuthBootstrapTx` retries on a fresh snapshot. Only the
membership rows are locked, not `users`: this is the authentication path, and
locking identities would put invitation redemption in contention with every
concurrent write to the same users.

### Deactivating an identity is the same invariant, from the other side

Nothing above writes `organization_members` when an identity is deactivated.
`Service.DeleteUser` is a soft delete and `Service.SuspendUser` a status change;
both leave every administrative membership row standing while `findIdentity`
stops admitting the identity at all. Eligibility is what changes, and it changes
for every organization the identity administers at once — so the decision is not
a single-organization guard and cannot be expressed as one.

`Service.organizationsStrandedByDeactivation` is that decision. It lists the
organizations in which the identity is itself an eligible administrator
(`Store.ListAdministeredOrganizations`), takes each one's `administration:<org>`
lock in ascending organization id, and **re-reads until the roster comes back
unchanged with every organization in it already locked** — only then does it
decide. The locks are held for the rest of the transaction that writes the
status, which is what serializes a deactivation against a concurrent removal or
demotion in any of the same organizations.

**One pass is not enough, and the reason is the promotion exemption above.** A
promotion takes no administration lock, so the identity can be made an
administrator of an organization the loop has already read past. Deciding on an
organization whose lock is not held admits exactly the interleaving the lock
exists to stop: the promotion lands, a demotion in that same organization counts
this identity — still active, because the deactivating transaction has not
committed — and commits, and the deactivation then commits on top and leaves the
organization with nobody. Looping until the locked set covers the roster closes
it, because the demotion then waits on a lock the deactivation holds and
re-counts afterwards. `TestPromotionRacingDeactivationCannotStrandAnOrganization`
places both steps between specific reads rather than racing for them.

The lock order is established in the loop rather than inherited from the query,
because it is the loop that depends on it. A pass that discovers an organization
sorting below one already held does acquire out of that order — unavoidable,
since a transaction-scoped advisory lock cannot be released and retaken — so two
deactivations that discover each other's organizations mid-loop can deadlock.
PostgreSQL detects that and aborts one loudly, which is the failure to prefer
over the silent stranding it replaces, and it takes a promotion landing inside
both loops to reach at all.

**Stranded takes two conditions, and the second is the point of the rule rather
than a softening of it** (`OrgAdministration.StrandedByDeactivating`): the
identity is the organization's only eligible administrator, **and** somebody
else in that organization can still authenticate. `RegisterUser` gives every
identity a personal organization it solely owns, so counting administrators
alone would refuse every deletion on this platform — an ordinary member's
included. What the invariant protects is the members who would be left in an
organization nobody can administer, so it asks whether there are any. An
organization nobody else is in is left empty, not unadministrable.

A user-scoped transaction can resolve none of this on its own:
`organization_members` is scoped to `app.current_org_id`, which a deactivation
does not set, and `users` is readable only for the caller's own row. Both fail
*silently* — zero rows, no error — and "administers nothing" is exactly the
answer that lets the deactivation through. So request traffic resolves it through
`identity_administered_organizations`, a `SECURITY DEFINER` function owned by
`app_control_plane` and scoped to the caller's **own identity** (migration 136),
and a transaction that spans tenants evaluates the same predicate directly.
`ListAdministeredOrganizations` branches on which of the two it is in and
refuses a transaction that is neither, rather than returning the empty answer.

**The two entry points differ in what they do with the result, deliberately:**

| Entry point | Disposition |
| --- | --- |
| `Service.DeleteUser` | **Refused** — `business.ErrIdentityAdminContinuity`, carried by `IdentityAdminContinuityError`, which names the organizations and maps to `FailedPrecondition`. Offboarding an administrator is a handover first and a deletion second, and the caller is told which organizations are waiting on one. |
| `Service.SuspendUser` | **Permitted, and recorded.** Suspension is how a compromised account is contained; an invariant about who can administer an organization must not be the reason a credential in somebody else's hands stays live. The stranded organizations go onto the `user.suspended` audit event as `organizations_without_administrator` and into the operator notification. The locks are still taken — they are what makes the recorded list the one that actually committed. |

Reactivation (`Service.UnsuspendUser`) can only raise an eligible-administrator
count, so it is settled from its argument like a promotion and takes no lock.

**What this does not cover, and cannot.** In-tree, `users.status` is written in
exactly two places — `PostgresStore.DeleteUser` behind `Service.DeleteUser`, and
`PostgresStore.UpdateUserStatus` behind `SuspendUser`/`UnsuspendUser` — and both
go through the guard. `PrivacyWorkflow.Delete` (`pkg/business/gdpr.go`) is the
exception: the interface is implemented by the host, not here, so an
implementation that deactivates the identity itself bypasses this entirely, and
erasure is precisely where an organization's sole administrator is most likely to
go. The guard is deliberately **not** applied around that call. Erasure runs on
durable leases with a retry-and-fail ladder, so a precondition refusal there
would consume attempts and land a legally mandated request in `GDPRFailed`,
blocked on an unrelated organization's staffing. A host integrating a privacy
workflow owns that decision and should make the handover before the erasure,
rather than discover the invariant from a failed job.

### `organizations.owner_id` is provenance, not authority

`owner_id` is the owner of record. It is written exactly once, when the
organization is created, and it is read for display and to address the billing
contact. **No authorization decision anywhere derives from it** — every one
resolves against `organization_members`. Installation ownership eligibility,
organization permission checks, and the admin gates all read the live
membership row.

So: **ownership transfer is not required before the owner of record is removed
or demoted**, and the two are deliberately not reconciled. Demoting the owner of
record strips their authority immediately, because their authority was never in
`owner_id` to begin with; `owner_id` keeps recording who created the
organization, which is a fact about the past that a role change does not make
untrue. Silently repointing it at another member would move a record of
accountability to someone who never accepted it, and doing the reverse — making
`owner_id` confer authority — would grant a permission no one granted.

The two can therefore disagree on live data, and that disagreement is reported
by the membership integrity diagnostic rather than repaired in place.

## Scoped roles downstream: two paths, and when to use which

A product is many backend services, each with per-module roles. A downstream
service authorizing on a **scoped** role assignment
(`role_assignments.scope IS NOT NULL`, e.g. `analyst` on `module-a`) has two
ways to learn the caller's grants. Both are first-class; pick by freshness need.

### Path A — the `X-Scoped-Roles` header (fast, token-fresh)

At mint / refresh / org-switch, accounts resolves the caller's **direct,
principal-subject** scoped assignments in the active org and stamps them onto
the access token as the compact `sr` claim:

```json
"sr": { "module-a": ["analyst"], "module-b": ["admin", "editor"] }
```

The auth-gateway forwards this to downstream services as the JSON
`X-Scoped-Roles` header (like `X-Org-Role` / `X-Platform-Role`). A service reads
it from the request context alone — no callback to accounts:

```go
// The two-value return prevents reading a miss as a denial: on a truncated
// header (conclusive == false) the caller falls back to the authoritative path.
granted, conclusive := adapters.HasScopedRole(ctx, "module-a", "analyst")
switch {
case granted:
    // allow
case !conclusive:
    // header incomplete — consult CheckPermission (Path B)
default:
    // deny
}
```

Properties and limits:

- **Freshness:** as fresh as the token. A scoped-assignment change revokes the
  caller's sessions (migration 91, mirroring how `organization_members` changes
  revoke sessions in migration 70), so a live token's `sr` claim is never
  staler than one refresh cycle — but a long-lived, un-refreshed token can lag.
- **Direct principal grants only.** Team-inherited grants and org-global
  (NULL-scope) roles are **not** in the claim — those stay on Path B, which
  honors team inheritance and wildcards.
- **Bounded, and truncation is signalled.** The claim carries at most
  `auth.MaxScopedRoleAssignments` (64) scoped pairs so token size stays
  predictable. A caller with more keeps a bounded slice and the token sets the
  `srt` claim (`X-Scoped-Roles-Truncated: true`); the mint does **not** fail, so
  the user is never locked out. When that flag is set, an absent grant in the
  header is *unknown*, not a denial — the service must consult Path B. Use
  `adapters.ScopedRolesTruncatedFromContext(ctx)` to detect it.

### Path B — `PermissionService.CheckPermission` (authoritative, current)

For callers that must have the current answer — long-lived jobs whose token
predates a role change, or high-sensitivity operations — call the oracle:

```go
client := authzclient.New(conn, os.Getenv("CODEFLY_INTERNAL_TOKEN"))
resp, err := client.CheckPermission(ctx, &accountsv1.CheckPermissionRequest{
    SubjectId: userID, SubjectKind: accountsv1.SubjectKind_SUBJECT_KIND_PRINCIPAL,
    Resource:  "deployments", Action: "write",
    OrgId:     orgID, Scope: "module-a", // empty scope = any scope
})
```

`CheckPermission` is `EXPOSURE_INTERNAL` (`generated/authz-methods.json`): it is
served only on the internal transport and requires the service credential
(`X-Codefly-Internal-Token`). A caller without it is refused before the handler
runs; a tenant transport refuses the method outright. It honors the full RBAC
model — team inheritance, wildcard `resource`/`action`, and `scope` (a NULL
assignment scope matches any requested scope). Polyglot (Python/TS) services
call the same RPC over gRPC/Connect with the credential in metadata.

### Which to use

| | Path A — header | Path B — CheckPermission |
|---|---|---|
| Cost | zero round-trips | one internal RPC |
| Freshness | token-fresh (≤ 1 refresh) | live, authoritative |
| Covers | direct scoped grants | + team inheritance, wildcards, NULL-scope |
| Reach for it when | per-request checks on the hot path | long-lived jobs, high-sensitivity ops, non-scoped RBAC |

Default to Path A on the request hot path; escalate to Path B when a stale
answer is unacceptable.

## Implementation status

| Layer | Status |
|---|---|
| L1 Policy gates | ✅ Live. All admin RPCs gated. Webhook + api-key handlers added scope checks 2026-04-26. |
| L2 Permissions | ✅ Live. `CheckPermission` + RBAC tables. Wildcard + team inheritance. |
| L3 RLS | ✅ Live across **all** per-tenant and per-user relations, **fail-closed** end-to-end. Connection-level role downgrade (`BeforeAcquire` SET ROLE app_tenant) makes un-wrapped Store calls return zero rows by default. `WithOrgTx` / `WithUserTx` / `WithControlPlane` helpers, `app_tenant` and `app_control_plane` roles. Integration tests prove cross-tenant blocking + fail-closed-on-unwrapped across direct-org, JOIN, polymorphic, and self-referential policies. The authoritative relation inventory is [module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md). |

### RLS coverage (table → migration)

**Historical adoption record.** This table records how the first policies were
rolled out, migration by migration, and is kept for that history. It is *not*
the current inventory and has not been maintained as one — `audit_export_configs`
below was dropped by store migration 102, and `usage_records` was replaced by
`usage_events` + `usage_totals` in `60_usage_metering`. For what is protected
today, read the scope inventory in
[module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md), which is held to
the executable inventory by a test.

| Table | Policy shape | Migration |
|---|---|---|
| `audit_export_configs` | direct org_id | 23 |
| `webhook_subscriptions` | direct org_id | 27 |
| `webhook_deliveries` | JOIN via subscription | 27 |
| `api_keys` | direct organization_id | 28 |
| `org_settings`, `invitations`, `organization_members`, `subscriptions`, `entitlement_overrides`, `usage_records` | direct org_id | 29 |
| `teams` | direct org_id | 30 |
| `team_members` | JOIN via teams | 30 |
| `audit_events` | polymorphic (nullable org_id; NULL-org rows readable only under the control plane, and writable by it or by the user-scoped transaction whose own `actor_id` they carry) | 31, 122 |
| `roles`, `role_assignments` | polymorphic (built-ins NULL globally readable) | 32 |
| `organizations` | self-referential (id matches setting) | 33 |
| `org_identity_providers` | direct org_id (pre-auth discovery via control-plane) | 92 |

### Skip-list — superseded

The user-scoped half of this list is gone: `users`, `mfa_devices`,
`notifications`, `sessions` and their siblings carry forced RLS policies today
and enter through `WithUserTx` (`app.current_user_id`), so `WHERE user_id = $1`
is no longer the safety property. `oauth_state` and `refresh_tokens` no longer
exist at all. `role_permissions` gained a parent-visibility policy in
`65_role_permissions_rls`.

What remains outside RLS is the global catalog scope, and only that. It is
listed — with every other relation and its required boundary — in
[module/DATABASE_AUTHORITY.md](./module/DATABASE_AUTHORITY.md).

## How the role-downgrade works

Codefly's Postgres plugin connects the api as a superuser. Postgres
superusers bypass RLS unconditionally, even with FORCE ROW LEVEL
SECURITY — so naive RLS is silently defeated. We solve this without
changing codefly's Postgres plugin:

```
                     pgxpool.Config.BeforeAcquire
                              ↓
   superuser conn ────► SET ROLE app_tenant ────► caller
                                                     │
                              ┌──────────────────────┤
                              ▼                      ▼
                       ┌─────────────┐         ┌──────────────────┐
                       │ WithOrgTx   │         │ WithControlPlane │
                       │ WithUserTx  │         └──────────────────┘
                       └─────────────┘                │
                              │                       ▼
                              ▼            SET LOCAL ROLE app_control_plane
              SET LOCAL                    (a named role with BYPASSRLS and
              app.current_org_id = X        its own explicit grants — NOT
              app.current_user_id = U       the session superuser)
              (still app_tenant —
               policy filters by scope)

                              ↓                       ↓
                       RLS applies             RLS bypassed by role
                       fail-closed             (audited, grant-limited)
```

`BeforeAcquire` runs on every connection checkout — every operation
the api performs (request-path or worker) starts as `app_tenant`.
`WithOrgTx` and `WithUserTx` add the scope filter; `WithControlPlane` assumes
`app_control_plane` via `SET LOCAL ROLE` for the tx duration. All three
unwind on commit/rollback. `AfterRelease` does `RESET ROLE` as a
safety net before the connection returns to the pool.

The fail-closed property: a Store method called WITHOUT either
wrapper runs as `app_tenant` with no `app.current_org_id` set.
RLS policies see neither match, return zero rows. A bug that forgot
to wrap surfaces in tests as "expected 1 row, got 0" — loud, not
silent.

## Built-in role catalog import

Migration 4 seeds `admin` / `editor` / `viewer` as hand-written SQL. Products
that already maintain a permission catalog outside this repo — a machine-readable
list of roles and `resource:action` grants reviewed in their own CI — can sync it
into the L2 tables without forking migrations, using the catalog importer.

> This is **not** the generated authorization catalog in
> `module/AUTHORIZATION_CATALOG.md`. That projects per-RPC *method policy*; this
> one seeds *RBAC roles*.

### Catalog format (versioned JSON)

```json
{
  "version": 1,
  "roles": [
    {
      "name": "module-a:analyst",
      "description": "Read access to module A",
      "scope": "module-a",
      "permissions": [
        {"resource": "reports", "action": "read"},
        {"resource": "queries", "action": "execute"}
      ]
    }
  ]
}
```

- `version` must be `1`. `resource`/`action` accept `*` for wildcard grants.
- `scope` is stored on the role (`roles.scope`) as the default
  `role_assignments.scope` for later assignments. Deriving it at assignment time
  lands with strict scope semantics; until then the column is recorded but not
  yet consulted by the assignment path.

### Semantics

- **Upsert built-in roles keyed by name** (`built_in = true`, `org_id IS NULL`).
  A role the catalog names but that a hand-seeded built-in already occupies
  (e.g. `admin`) is *adopted* into catalog management.
- **Diff-apply permissions** — only the `(resource, action)` rows that differ
  are inserted or deleted, so changing one permission is exactly one row change.
- **Provenance bounds deletion.** Only `roles.catalog_managed = true` rows are
  removal candidates; a catalog that doesn't mention `admin`/`editor`/`viewer`
  leaves them alone. Org-defined custom roles (`org_id` set) are never touched.
- **One `system`-actor audit event per applied change** (`saas.role.created` /
  `saas.role.updated` / `saas.role.deleted`, `org_id` NULL), stamped with the catalog's
  SHA-256 (`catalog_sha256`) and source label (`catalog_source`) so a change is
  traceable to the exact catalog version that produced it.

### Safety

- `-dry-run` prints the byte-stable plan and writes nothing.
- A removal that would cascade away existing `role_assignments` is **refused**
  unless `-force` (which then deletes those assignments along with the role).
- A catalog that declares **no roles at all** would remove every catalog-managed
  role — almost always a truncated or empty file rather than an intentional
  "delete everything", and one the assignment guard above can't catch for roles
  without assignments. It is refused unless `-force`.
- Same catalog in → same DB state out; a second run is a no-op.

### Workflow

Built-in roles can only be written with RLS bypassed (migrations 32, 65), so the
importer runs under the audited `app_control_plane` role. The connection
principal must be a member of that role (the same authority migrations run
under).

```sh
# from module/services/accounts/code
go run ./cmd/role-catalog-import -catalog roles.json -database-url "$DATABASE_URL" -dry-run
go run ./cmd/role-catalog-import -catalog roles.json -database-url "$DATABASE_URL"
```

Domain core is `pkg/rolecatalog` (parse + diff + deterministic plan, no DB);
`pkg/infra` snapshots current state and applies the plan in one transaction.

### Composed contribution catalog (deploy step)

A consuming solution does not hand-write the catalog. It ships a
`PermissionsContribution` (`codefly/saas/permissions-contribution/v1`), and
`module-compose` regenerates the catalog the importer applies:

1. The contribution declares `resource:action` permissions under a namespace
   reserved to that solution. `module-compose` merges every contribution into
   the permission vocabulary (`deployment/generated/contributed-permissions.json`,
   `pkg/permissioncatalog/catalog_gen.go`) and, from the same input, emits the
   role catalog `deployment/generated/contributed-roles.json` in the versioned
   format above (`module/tools/composition/composition.go`).
2. A permission exists in the RBAC schema only as a grant inside a role
   (`role_permissions` has no standalone permission registry), so the bridge
   materializes one built-in role per contributing namespace —
   `name: "<namespace>:catalog"`, `scope: "<namespace>"` — granting that
   namespace's full permission set. Each grant is stored verbatim as the
   contributed `{resource, action}` (the `resource` is namespace-qualified,
   e.g. `reference.console`). This is the authority the Work Context layer
   reads as a `WorkContextScope` (`resource_kind` + `actions`); mapping the
   stored grant onto that scope shape is the enforcement layer's concern
   (#415/#416) — this bridge lands the grants, it does not itself transform
   them. Finer-grained roles remain an org's own custom roles, which the
   importer never touches.
3. Both generated files are **base** — base-manifest-tracked
   (`module/tools/base-manifest.json`). A consumer cannot hand-edit them: a
   permission or role reaches a deployment only through contribution →
   regeneration, and editing the generated file without regenerating fails the
   base-integrity gate.

The composed `contributed-roles.json` is applied by `role-catalog-import` as a
bring-up step that runs **after** the store migration Job, under the same
`app_control_plane` connection — seeding the contributed roles automatically
instead of by the manual `go run` above. Its interaction with the importer's
empty-catalog guard is deliberate, not incidental:

- A module with **no** contributions emits `{"version": 1, "roles": []}`.
  Applied to a deployment that holds no catalog-managed roles yet, that is a
  clean no-op — nothing to create, nothing to remove — and exits `0`.
- Once contributions have seeded `<namespace>:catalog` roles, **regenerating
  back down to an empty catalog** (the last contributing solution was removed)
  makes the importer *refuse* rather than silently wipe them: an empty document
  is indistinguishable from a truncated file, so the guard demands `-force`.
  That transition is a genuine role removal, so the deploy step passes `-force`
  for it. The guard is not weakened for a generated catalog — emptying
  contributed authority is exactly the destructive step `-force` exists to
  confirm.

The module declares this step so the driver need not know the module's internals.
`deployment/topology.bindings.codefly.yaml` carries a top-level `deploy_jobs`
entry for `role-catalog-import`: it runs the `accounts` image (which ships the
importer binary and connects under the `app_control_plane` migration authority)
but *writes* to the `store` dependency and runs `after` the store — the boundary
a self-serving `bootstrap_job_endpoints` Job cannot express, since that models a
service reaching only its own endpoints. The generated bundle
([`deployment/README.md`](module/deployment/README.md)) surfaces it per
environment under `deployJobs`, resolving the catalog artifact, the store
endpoint/port, the `force` flag, and the ordering. The promotion driver mounts
the catalog, connects the store, runs the importer, and fails the promotion if
it exits non-zero — the same way it schedules the store migration Job it already
runs. It carries no repository, revision, or Argo resource.

## Anti-patterns to avoid

- **Don't replace L1+L2 with L3.** RLS is defense-in-depth; it's not
  a substitute for handler authz. A permission check at the handler
  produces a clean 403; a row that RLS hides looks like "not found",
  which makes UX/debug worse.
- **Don't forget WithOrgTx on per-tenant Store calls** once policies
  are live. Missing one returns zero rows in production — fail-closed
  but invisible. Catch with cross-tenant tests per Store method.
- **Don't WithControlPlane casually.** Every crossing is a layer-skip;
  legitimate cases are workers + platform admin only. Each call is counted
  (`recordControlPlane`), and `app_control_plane` holds only the grants it was
  given — so a careless crossing is visible and still bounded.
- **Don't omit the empty-orgID guard.** WithOrgTx rejects "" — this
  is the load-bearing check that prevents a missing-context bug from
  silently matching the empty tenant.
