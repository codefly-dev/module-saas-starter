# Database migrations

The store runs golang-migrate. Every numeric version must have exactly one
matching `.up.sql` and `.down.sql`. Shipped files are immutable: corrections
must be new forward migrations; there is no edit/delete exception policy.

Choose a version above the target branch's highest version, including gaps.
Concurrent schema changes must merge in numeric order. If a higher version
lands first, renumber the unshipped pair above the new frontier before retrying.
A successful clean install does not prove an existing database will execute a
lower-numbered migration.

The required **Base manifest integrity** context validates every PR and merge
queue candidate against its event's base SHA, using the proposed merged tree.
It rejects duplicate versions, missing pairs, changes to shipped files, and
new versions at or below that reference's frontier. The queue rechecks against
the base it will actually merge onto; a green PR run cannot reserve a number.

Migration, store runner/dependency, topology, and migration-gate/CI changes also
run PostgreSQL upgrade and clean-install checks. The upgrade installs the
reference tree, seeds a user, organization, membership, and team, then runs the
proposed runner and migrations. It records every completed added version,
checks fixture preservation, and compares the full schema dump (including
functions, policies, and grants) with a separate clean-install cluster. A
test-only migration exercises execution tracking and data backfill even when
only the runner changed. Domain-specific backfills still need their own tests
with representative historical data beyond this shared fixture.

From the repository root, with Go and Docker available:

```sh
node module/tools/migration-reference-gate.mjs origin/main
node module/tools/migration-reference-gate.mjs origin/main --replay
```

Reference validation compares committed trees. CI uses full Git history and
passes `pull_request.base.sha` or `merge_group.base_sha` directly, rather than
the feature branch's old merge base. Pushes use the previous commit; new tags
and manual runs without a previous commit validate the current tree.
