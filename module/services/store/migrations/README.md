# Database migrations

The store runs golang-migrate over **one generated baseline**: `1_baseline.up.sql`
is the schema, seed rows, roles, grants, policies and function owners of an empty
database, exported by `../tools/generate_baseline.py` from a disposable PostgreSQL
16 that had the previous ledger applied. `../baseline.provenance.json` records
which files it folded and their hashes. The ledger carries **no upgrade path**: a
database installed by an earlier ledger is recreated, never migrated forward, and
the runner refuses one rather than leave it behind (`code/main.go`,
`refuseForeignLedger`).

## Changing the schema

Add a forward migration above the baseline, as a matched `N_name.up.sql` /
`N_name.down.sql` pair with `N` above the current frontier. A shipped file is
immutable; corrections are new forward migrations. When the ledger has grown
enough to be worth reading as one schema again, fold it:

```sh
python3 module/services/store/tools/generate_baseline.py        # from the working tree
python3 module/services/store/tools/generate_baseline.py --from-ref <ref>
```

The generator replaces every file here with a regenerated `1_baseline` and
rewrites the provenance. Never edit the baseline by hand: the provenance digest
refuses it (`qualification/managed-database/stage.py`), and so does the
reference gate.

## The two settings a policy reads

A tenant-scoped policy reads `codefly.current_tenant_id`, and a user-scoped one
`codefly.current_user_id`. These are not names this repository chose: they are
the defaults of the platform's Postgres library
(codefly-dev/service-postgres, `libs/go/store.go`), which is what binds them for
a transaction opened through its scoped factory, and what every other store on
the platform reads back. `65_library_scope_settings` renamed the host's own
`app.current_org_id` / `app.current_user_id` onto them, over the 75 policies, 6
audit partitions and 9 function bodies that read them.

A consumer's own migration must use the same two names. A policy on any other
setting fails **quietly**: nothing binds it, `current_setting(…, true)` returns
NULL, the predicate is false, and the relation reads as empty for every caller
rather than raising. `../../../tools/rls-migration-gate.mjs` is what refuses
that, and it accepts only these two.

## What CI proves

The required **Base manifest integrity** context runs
`module/tools/migration-reference-gate.mjs` against the event's base SHA. A forward
migration must exceed the reference frontier, and no shipped file may change or
disappear. A fold is the one legal deletion, and only when declared: the provenance
must name every reference file by content and none may survive.

When the change touches the ledger, the runner, the topology or the gates, CI
replays it on real PostgreSQL 16 clusters (`code/migration_upgrade_test.go`). A
forward migration is proved as an upgrade — install the reference, seed
representative tenant rows, apply the proposal, compare with a clean install and
check nothing was lost. A fold is proved as equivalence — a clean install of the
reference ledger and of the proposal must dump to the same schema and carry the
same seed rows. Both prove that a database installed by a ledger the proposal
does not contain is refused.

From the repository root, with Go and Docker available:

```sh
node module/tools/migration-reference-gate.mjs origin/main
node module/tools/migration-reference-gate.mjs origin/main --replay
```

[`../test-targets.json`](../test-targets.json) declares these as two planner
targets; `module/tools/test-targets-gate.mjs` holds the declaration to the gate's
replay scope in both directions.

Reference validation compares committed trees. CI passes `pull_request.base.sha`
or `merge_group.base_sha` directly rather than the feature branch's old merge
base. Pushes use the previous commit; new tags and manual runs without a previous
commit validate the current tree.

## Pre-renumber local and validation databases

Local and validation databases created before the authority-interface branch
renumbered migrations 46–49 to 49–52 **must be recreated**. They predate the
migration-name stamp, so the name guard cannot identify them reliably. This
ledger provides no upgrade path for those databases. Do not mark a failed
migration clean and continue: recreate the database through the host's setup.
The runner includes this remedy in migration failures.
