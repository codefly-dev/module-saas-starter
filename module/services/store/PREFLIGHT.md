# Accounts preflight package

`preflight/` ships Accounts' preinstall database contract alongside the migration
sources. A migration-package builder includes these three ordinary files:

- `manifest.json`: `codefly.dev/postgres-preflight-package/v1`, semantic package
  version and SHA-256 hashes of the other two files.
- `policy.json`: `codefly.dev/postgres-empty-baseline-policy/v1`, naming the
  database, schema, runtime identity references, application roles, enum collision
  names, extensions, ledger column/type contract and required membership flags.
- `observe.sql`: the owner-reviewed read-only catalog query. The runner binds only
  declared placeholders from approved policy, applied database identities and
  the primitive's migration plan.

The builder records the exact clean Accounts source commit and content hashes,
ships `/package/preflight/` in the migration image and retains the same data in
review metadata. A service schema update changes this package and its version.
The generic operator does not carry an Accounts role/type inventory or SQL copy.
It must never execute Python or other plugins from a service package.

Package metadata is reviewed deployment input; its digest is a content binding,
not proof of an independent signature. The deployment's normal review and
artifact provenance process establishes who approved the source. A mutable
branch name or uncommitted package is not a package identity.

The supported SQL grammar uses ordinary quoted literals/identifiers, line
comments, the fixed ledger client-command sequence and catalog functions only.
Arbitrary function calls, schema-qualified shadows, transaction escapes, custom
casts, dollar quoting, block comments and extra client commands are refused.
Adding a catalog function requires a generic executor review; revising the
service role/type inventory does not.

The generic runner enforces PostgreSQL 16+, one repeatable-read read-only
transaction ending in rollback, the fixed timeout/client settings, bounded
catalog collections and logs, a fresh observation, and exact database/login/
instance/handoff binding. Group names come from the primitive's approved plan;
this package never derives managed access-role names. The package can strengthen
the runner's privilege limits but cannot admit a superuser, BYPASSRLS or elevated
runtime identity, or allow runtime membership to administer roles.

Version 1 preserves the existing empty-baseline decision: the owning migrator
must have usable database/schema ownership and the declared administrative
capability, runtime logins and any existing access groups must satisfy their
restricted flags, and existing membership options must match exactly. A dirty,
populated, empty or unqueryable migration ledger, application role/object/type
collision, unexpected group-parent membership, or unproven trusted extension
creation requires explicit disposition. It never authorizes replay, repair,
role changes, migration execution or ownership transfer. `baseline_candidate`
is a necessary precondition, not successful installation or proof of absence of
concurrent effects.

`database`, role inventories, enum names, required extensions and ledger shape
belong here. Catalog observation and role-name construction may eventually move
into a published database primitive; do not replace them with another group
hash or permission algorithm in a consumer. New primitive schema-plan versions
and delegated-reader changes are separate reviewed dependencies.

The existing typed receipt has `assessment` (`baseline_candidate` or
`catalog_requires_disposition`) and a sorted, duplicate-free `blockers` list.
Target mismatch, malformed data or unsafe observation transport fail before
classification. A service version cannot silently change that wire contract;
an incompatible evaluator change requires a new supported policy contract.

Validate owner inventory and package integrity with `go test` from `code/`.
The paired generic-observer tests exercise the original negative/positive
catalog matrix and demonstrate adding an Accounts role/enum without editing the
operator. Qualification against a real isolated PostgreSQL catalog is separate
from executing migrations or qualifying a hosted cluster.
