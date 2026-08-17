# Database authority

Status: runtime-role baseline, ownership separation, complete relation
inventory, exact request/control-plane/worker grants, and removal of the
application-settable RLS bypass are implemented. Physical credential isolation
remains open under `P2-DB-002` and `P2-DB-006`.

The Starter owns database roles, grants, RLS policy shape, and worker authority.
Installed products add tables through additive migration sources and must opt
each relation into a role explicitly. They do not widen the Starter's default
privileges or reuse a worker role for unrelated work.

## Roles

| Role | Login | RLS bypass | Intended authority |
|---|---:|---:|---|
| migration principal | yes | owner | Schema changes, role/grant reconciliation, migrations only |
| `app_tenant` | no | no | Tenant and user request transactions constrained by forced RLS |
| `app_control_plane` | no | yes | Audited pre-auth, bootstrap, platform administration, retention, and cross-tenant account operations |
| `app_billing_worker` | no | yes | Stripe subscription catalog reads and reconciliation writes only |
| `app_webhook_worker` | no | yes | Webhook subscription reads and delivery-history projection only |
| `app_job_worker` | no | yes | Product-neutral job messages, attempts, transitions, and scoped enqueue operation only |

All application roles are `NOLOGIN`, `NOINHERIT`, `NOSUPERUSER`,
`NOCREATEDB`, `NOCREATEROLE`, and `NOREPLICATION`. The tenant role cannot
assume the control-plane or any worker role. Roles use `BYPASSRLS` only for
operations that inherently span scopes; explicit grants remain the second
enforcement boundary. `app_control_plane` has DML on every non-worker,
non-job-platform application relation and no job relation or lifecycle
authority. Its only job-platform capability is the narrowly checked global
outbox enqueue operation described below.
The Codefly-managed runtime session is separately tested as non-superuser,
non-`BYPASSRLS`, unable to create roles/databases, and not the owner of any
application relation. Every application relation has the same owner as the
migration ledger, and application roles likewise own no relations.

## Fail-closed migration rule

Migration `61_runtime_role_baseline` removes tenant `TRUNCATE`, schema creation,
temporary-table authority, and all automatic future table/sequence grants.
`TRUNCATE` is forbidden to the tenant role because PostgreSQL does not apply RLS
to it.

Every new table migration must therefore:

1. classify the relation as tenant, user, global catalog, or worker-owned;
2. enable and force RLS for tenant/user relations;
3. create operation-specific policies without an application-settable bypass;
4. grant only the required operations to `app_tenant`, `app_control_plane`, or
   one named worker role;
5. add grant, policy, and cross-role tests in the accounts infrastructure suite.

There are no default application-role privileges to catch a forgotten grant.
A missing grant fails deployment tests or the first operation loudly instead of
making an unreviewed table writable.

## Global catalog grants

Migration `62_global_catalog_grants` removes the historical full-CRUD grant
from every relation that intentionally has no tenant RLS. Forward migration
`95_feature_flags_read_only` additionally removes all runtime write authority
from the retired feature-flag inventory.

| Relation | `app_tenant` authority | Other authority |
|---|---|---|
| `identity_providers` | select | migration principal writes |
| `plans`, `plan_entitlements` | select | migration principal writes; billing worker selects `plans` |
| `email_templates` | select | migration principal writes |
| `data_retention_policies` | select | migration principal writes |
| `bootstrap_state` | select, update | migration principal seeds |
| `feature_flags` | select | migration principal writes; runtime inventory is read-only |
| `platform_admins` | select, insert, update, delete | platform-super-admin/bootstrap application gates |

The infrastructure suite checks every operation in this matrix, including the
absence of `TRUNCATE`.

## Tenant and user relation grants

Migrations `63_request_relation_grants`, `64_delegation_grant_authority`, and
`66_team_member_upsert_authority`
replace historical full CRUD with the following exact `app_tenant` authority.
All tenant/user rows remain constrained by their forced RLS policies.

| Authority | Relations |
|---|---|
| select, insert | `audit_events`, `role_permissions`, `usage_events` |
| select, insert, update | `api_keys`, `delegation_grants`, `entitlement_overrides`, `invitations`, `org_settings`, `organizations`, `principals`, `subscriptions`, `usage_totals`, `webhook_deliveries` |
| select, insert, delete | `role_assignments`, `roles` |
| select, insert, update, delete | `audit_export_configs`, `organization_members`, `team_members`, `teams`, `webhook_subscriptions` |
| select, insert, update | `gdpr_requests`, `onboarding_progress`, `sessions`, `users`, `webauthn_ceremonies`, `webauthn_credentials` |
| select, insert, update, delete | `mfa_backup_codes`, `mfa_devices`, `notifications`, `user_identities` |
| insert only | `mfa_login_transactions` |
| no tenant relation authority | `job_attempts`, `job_messages`, `job_state_transitions`, `magic_links` |

Retention deletes and token-based pre-auth reads/updates use the audited
control-plane boundary; they are intentionally not granted to `app_tenant`.
The infrastructure suite compares PostgreSQL's complete public-table inventory
to this executable matrix, so adding a table without classifying and granting
it fails the gate.

## Scope and RLS inventory

| Scope | Relations | Required database boundary |
|---|---|---|
| global | `bootstrap_state`, `data_retention_policies`, `email_templates`, `feature_flags`, `identity_providers`, `plan_entitlements`, `plans`, `platform_admins` | No RLS; exact grants |
| tenant | `api_keys`, `audit_events`, `audit_export_configs`, `delegation_grants`, `entitlement_overrides`, `invitations`, `org_settings`, `organization_members`, `organizations`, `principals`, `role_assignments`, `role_permissions`, `roles`, `subscriptions`, `team_members`, `teams`, `usage_events`, `usage_totals`, `webhook_deliveries`, `webhook_subscriptions` | Enabled and forced RLS with at least one policy |
| user | `gdpr_requests`, `mfa_backup_codes`, `mfa_devices`, `mfa_login_transactions`, `notifications`, `onboarding_progress`, `sessions`, `user_identities`, `users`, `webauthn_ceremonies`, `webauthn_credentials` | Enabled and forced RLS with at least one policy |
| pre-auth | `magic_links` | Enabled and forced RLS; fail-closed request policy, accessed only by the control-plane role |
| job platform | `job_attempts`, `job_messages`, `job_state_transitions` | No request relation grant; function-only scoped enqueue plus exact job-worker grants |

Migration `65_role_permissions_rls` closes the former child-table gap:
`role_permissions` reads follow parent-role visibility, while inserts may target
only a custom role owned by the current tenant. The executable inventory checks
scope, `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY`, and policy
presence for every public application table.

## Generic job platform

Migration `72_job_platform_contract` gives `app_tenant` no direct relation
privileges on the common job tables. Request traffic may only execute the
`enqueue_job_message` security-definer operation, which accepts outbox work and
checks the actual caller role against the transaction-local organization or
subject scope before insertion. The table's forced insert policy remains a
second boundary. Migration `75_email_job_convergence` additionally permits
`app_control_plane` to execute that operation for global outbox work only, so a
pre-authentication product row and exact external-delivery command can share
one audited transaction. It still cannot enqueue inbox or tenant/subject work,
read payloads, mutate lifecycle, or replay. Billing and webhook projection
workers have neither relation nor operation authority.

`app_job_worker` has `SELECT`, `INSERT`, and `UPDATE` on messages and attempts,
`SELECT` on transition history, and execution of the enqueue operation for
privileged inbox/global producers. It alone may execute `replay_job_message`,
which accepts dead-lettered sources only and copies payload bytes without
returning them through the administration API. It has no delete authority and no product
relation grants. Executable tests pin the role attributes, exact table/function
ACLs, request denial, control-plane global-outbox-only authority, scope checks,
and product-table isolation. See `JOBS.md` for the generated producer and
lifecycle contract.

`app_webhook_worker` has `SELECT` on both webhook relations and column-level
`UPDATE` only for delivery status, HTTP outcome, attempts, and timestamps. It
cannot rewrite subscription/event identity or exact payload bytes and has no
insert, delete, truncate, schema, temporary-table, job-platform, or unrelated
product authority. The
generic `app_job_worker` owns the inverse execution boundary and cannot access
either webhook relation. Migration `73_outbound_webhook_job_convergence`
removes the former queue lifecycle columns for upgraded databases; migration
`74_webhook_projection_column_authority` pins the immutable-column boundary.

## User directory operations

Migration `69_user_directory_operations` replaces the historical
`users_select USING (true)` policy. Request traffic can read only the row whose
UUID matches `app.current_user_id`; pre-authentication, platform administration,
and workers use their separately named roles.

Tenant code does not receive co-member visibility over the full `users` row.
The `organization_member_primary_email(user_id)` operation returns exactly one
field, only when that user belongs to `app.current_org_id`. It is a
`SECURITY DEFINER` function owned by the NOLOGIN `app_control_plane` role,
revokes public execution, and grants only `EXECUTE` to `app_tenant`. Executable
tests pin its owner, security mode, ACL, cross-user isolation, and membership
filter. User-owned identity deletion is separately granted for atomic GDPR
deletion and remains constrained by `user_identities_user` RLS.

## Session authorization invalidation

Migration `70_session_authorization_invalidation` makes refresh-session
invalidation a database invariant rather than a handler convention. Changes to
user status, organization membership or role, platform role, and verified MFA
enrollment revoke exactly the affected active refresh sessions in the same
transaction. Organization mutations target sessions selected into that tenant
plus org-less sessions; user-wide facts target every family.

The shared trigger function is `SECURITY DEFINER`, owned by the NOLOGIN,
`BYPASSRLS` `app_control_plane` role so tenant- and user-scoped mutations can
reach the narrowly selected session rows without widening request-role RLS.
Public and `app_tenant` execution are revoked. Executable tests pin the owner,
security mode, ACL, tenant scoping, user scoping, and transaction rollback.

## Session lifetime and device admission

Migration `71_session_lifetime_and_device_context` adds an explicit
`idle_expires_at` to
every refresh-session row and preserves bounded device display context across
the durable MFA handoff. The existing `expires_at` is the fixed absolute family
boundary; refresh successors inherit it and the original `created_at`, while
only `last_active_at` and `idle_expires_at` advance.

Initial session insertion locks the owning user row before evaluating active
families. This serializes concurrent logins across replicas, retires already
expired families, and revokes the least-recently active family before inserting
when the configured per-user device cap is full. Refresh rotation bypasses
admission because it replaces a row inside an existing family. Device metadata
is display/audit context only and never participates in authentication,
authorization, or tenant isolation.

Organization selection uses the same control-plane session boundary. Accounts
locks the exact active `sessions` row identified by the independently verified
user and access-token `sid`, verifies the target `organization_members` row,
resolves current tenant/platform/MFA authorization, and updates only `org_id`,
`org_role`, and `platform_role`. The refresh hash, family id, device context,
creation/activity timestamps, idle expiry, and absolute expiry are unchanged.
This lock order serializes switch-versus-refresh races without treating a stale
access-token session id as refresh replay.

## Control-plane boundary

Migrations `67_control_plane_role` and `68_remove_policy_guc_bypass` replace
the former custom-setting capability with `app_control_plane`. The authored
deployment topology lists that role under Postgres
`runtime-read-write-roles`, and `WithControlPlane` uses
`SET LOCAL ROLE app_control_plane`. Active policy expressions are tested to
contain no `app.bypass` reference; setting an arbitrary custom GUC can no
longer grant row visibility.

The role is `NOLOGIN`, owns no relations, receives no implicit future grants,
cannot create schema or temporary objects, cannot truncate, cannot assume a
worker role, and has no job relation, lifecycle, or replay access. Its exact
relation and function matrix and the managed session's non-owner/non-superuser
attributes are executable infrastructure tests.

The remaining boundary is physical credential separation. Today one
Codefly-managed read-write principal is a member of all application roles so
the accounts monolith can run request, pre-auth, and scheduled control-plane
paths. Codefly Postgres should expose role-specific connection secrets—and the
Starter should split the corresponding pools/processes—so a request-only
credential has no `SET ROLE` path to privileged roles. Track that PaaS work
under `P2-DB-002`; do not compensate with a public endpoint, superuser
credential, or application-settable policy flag.
