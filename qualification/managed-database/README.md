# Managed PostgreSQL qualification

Read the [installation contract](../../module/services/store/baselines/managed-v1/CONTRACT.md).
This qualification uses disposable local PostgreSQL 16 containers with networking
disabled. It never reads a database DSN from the environment and does not touch
managed infrastructure. Docker must have the exact image in `run.py` cached.

Run the source checks and real database suite:

```sh
python3 -m unittest discover -s qualification/managed-database -p 'test_*.py'
python3 qualification/managed-database/ci.py /tmp/managed-database.json
```

`ci.py` verifies the published migration primitive module checksum and builds its
pinned golang-migrate CLI for Linux amd64. `run.py --migrate /absolute/migrate
--output /tmp/result.json` accepts an already qualified binary. Both fresh and
canonical paths run migrations through the real ledger. The suite compares
schema, ACLs, function ownership and seed semantics; exercises all runtime role
boundaries, request-scope guards, revocation and custody immutability; and checks
fresh-install refusal, legacy rollback, managed rollback refusal, failed and
interrupted migration cleanup. Disposable superuser sessions only provision the
fixture and perform independent observations. Fresh migrations run as a
NOSUPERUSER/NOBYPASSRLS/CREATEROLE identity.

To additionally qualify the supported managed-bootstrap/access reconciler, stage
and package both profiles using the reviewed schema-plan tool. The local template
must name database `users`, groups `example_ro`/`example_rw`, principals
`example_reader`/`example_writer`, and all five canonical application roles in
`read-write-roles`. Fresh owner is `example_migrator`; canonical fixture owner is
`postgres`. Pass `--fresh-package /absolute/fresh --upgrade-package
/absolute/upgrade` to `run.py`. Packages contain the published managed-bootstrap
and migrate binaries, immutable `bootstrap/plan.json`/sources, and `binding.json`.
The suite runs bootstrap as OS UID 65532, checks access-committed receipts and
replay, and verifies that runtime logins cannot assume the migration owner.

For application-level custody and authenticated tenant TLS regression:

```sh
python3 qualification/execution-custody/run.py --managed-baseline
python3 qualification/execution-custody/run.py --managed-baseline --tenant-mount
```

These use real local Accounts/PostgreSQL/Vault (and Redis for tenant TLS), require
the images named by that runner, and keep transient credentials in the fixture.
They do not claim managed-provider, IAM, proxy or hosted execution qualification.
`--allow-dirty` is development-only; final evidence must pin a clean commit.

Regenerate the snapshot only when deliberately changing the versioned source:

```sh
python3 qualification/managed-database/generate.py
```

The generator reads the pinned historical Git tree into an empty local database,
exports deterministic natural-key seeds and install-time timestamps/partitions,
and emits the baseline, forward migration and provenance. Historical SQL is
never edited. Repeat generation must reproduce every emitted byte. Do not run it
against application data or use it as a deployment-time SQL transformer.
