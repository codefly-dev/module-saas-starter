# Managed PostgreSQL install contract, version 1

This opt-in fresh-install source targets PostgreSQL 16+ migration principals
with CREATEROLE, database/schema ownership and extension provisioning authority,
but neither SUPERUSER nor BYPASSRLS. It changes no historical migration bytes.
The normal migration source remains the upgrade path for existing installations.

The baseline is a reproducible export of the empty canonical database at
migration 135, including its seed data, exact grants, constraints, triggers,
function bodies and security-definer owners. It creates the five named runtime
roles as NOLOGIN, NOINHERIT, NOSUPERUSER, NOBYPASSRLS, NOCREATEDB,
NOCREATEROLE and NOREPLICATION. Only the migration principal receives explicit
SET membership; the existing access reconciler owns external runtime membership.
A fresh install rejects existing application objects and any existing named
runtime role. Extension-owned objects and the migration engine's own ledger are
allowed. There is no SQL text rewriting at deployment time.

Both paths apply migration 136, which adds explicit policies for the four
background roles on a fixed inventory of their existing RLS relations. Policies
require the exact current SQL role, not a client-settable setting or inherited
membership. Relation, column and function ACLs remain the permission boundary;
no privileges, tenant policies or FORCE RLS flags are broadened. A future table
requires an explicit policy as well as its own deliberate SQL grants.

Existing installations keep their existing role attributes: an unprivileged
migration principal cannot revoke BYPASSRLS from legacy roles. Migration 136
adds equivalent explicit row visibility without claiming to perform that
privileged attribute conversion. Existing roles may separately be reduced to
NOBYPASSRLS by their authorized provisioning owner after the policies qualify.
No new role grant or administrator credential is part of this contract.

Fresh installation stages the baseline at version 135, then canonical migration
136 and later migrations. The existing content-addressed schema-plan and
managed-bootstrap runner apply these through the normal store lineage and
schema_migrations ledger. They record the executed versions normally; there is
no force-version, fake migration replay or hand-populated ledger. Upgrade stages
the unchanged canonical migrations and migration 136. Profile selection must be
explicit and approval-bound; it is never inferred from a failed install.

The baseline is forward-only. Migration 136 can be rolled back only while all
four legacy roles retain BYPASSRLS; otherwise removing the policies would break
required background operations and rollback refuses. Restore from a reviewed
backup or apply a forward fix for a failed managed-baseline deployment.

Qualification must compare final catalog structure, seed data, ACLs and function
owners between canonical and fresh installs; run the migration engine under a
non-superuser/non-bypass login; prove ledger replay; and exercise all four
background roles, tenant isolation, role assumption, pre-auth lookup, revocation
triggers and custody immutability. Local evidence never establishes managed
provider qualification. New application and migration artifacts require their
own source pins, image receipts and separately reviewed deployment effects.

The baseline uses deterministic UUIDv5 identifiers for built-in catalog seeds
(roles, plans, email templates, retention policies), preserving their natural
keys and FK relationships. Seed timestamps and the initial audit partition
window are evaluated at installation time. The pinned local generator reads
historical SQL from its recorded Git revision; it is never run against a live
application database. Its output and provenance are reviewed source artifacts.

Three guarded SECURITY DEFINER operations need explicit DML owners:
`enqueue_job_message` and `replay_job_message` become owned by app_job_worker;
`publish_domain_event` becomes owned by app_control_plane. EXECUTE audiences
remain unchanged. The enqueue guard is corrected in migration 136 to reject
when the complete request-scope predicate IS NOT TRUE, including missing or
empty request context. Downgrade never restores the nullable guard. Their
old schema owner cannot supply implicit BYPASSRLS on the managed path. Existing
control-plane-owned functions retain their ownership. The membership-integrity
scan's guard now requires the exact app_control_plane role. Schema creation is
revoked from each incoming function owner after the transfer.

Migration 136 also revokes runtime grants on schema_migrations left by historical
default privileges. Downgrade deliberately does not restore this ledger write
capability. All other application relation and column grants are unchanged.

To prepare an approval-bound package input, use:

```
python3 qualification/managed-database/stage.py --profile fresh \
  --template-spec /reviewed/schema-spec.json --output /new/candidate
```

Use `--profile upgrade` for an existing installation. The existing supported
schema-plan packager consumes `/new/candidate/spec.json` and its `sql` directory.
Database, access groups and external principals come unchanged from the reviewed
template. No new operator or storage primitive is required: only explicit source
selection and resulting content hashes change. Never choose fresh as a fallback
for a dirty ledger, unknown installation state or failed upgrade.

External reader/writer logins are distinct from the NOLOGIN/NOINHERIT application
and access-group roles. The supported access reconciler grants group membership
without changing externally provisioned login attributes. The reader connection
uses the access group's SELECT grants through an inheriting membership; Accounts
does not set a reader session role or accept a DSN role override. Its external
login must be INHERIT when a new membership is granted, as in the published
primitive's fixture. On PostgreSQL 16+, verify the actual membership's
inherit_option, not just rolinherit: changing the latter does not update existing
edges. Writer-to-access-group membership may inherit, but access-group-to-app
memberships remain non-inheriting and require explicit SET ROLE. No external
runtime identity may inherit or assume the migration owner. Verify this effective
membership contract before hosted qualification; any repair to existing provider
memberships requires its own reviewed authority and effect.

`sync_webhook_event_subscriptions` retains its existing schema-owner identity:
no runtime role receives DELETE on event_subscriptions. One SELECT-only policy
on webhook_subscriptions names the exact retained function owner, resolved from
pg_proc rather than inferred from the migration actor. Its current_user predicate
is exact. This read visibility belongs to the owner role, not exclusively to the
function. Runtime logins must neither inherit nor SET ROLE to this identity.
The function retains its scope and endpoint checks, body and EXECUTE audience.
Rollback removes this policy only under the same legacy-BYPASS precondition.

A failed migration or refused downgrade leaves the ordinary ledger dirty; stop
and follow the existing dirty-migration recovery procedure. Never force a ledger
version or automatically switch profiles. The supported migration runner sends
each SQL version atomically, so its temporary CREATE grants, owner changes and
policies roll back together on failure. Access reconciliation runs only after
all lineages succeed. Local qualification deliberately exercises these cases.
