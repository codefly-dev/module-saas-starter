# AGENTS.md — store

The store is one generated baseline plus a forward-migration ledger
(`migrations/README.md` has the mechanics: numbering, folding, and what CI
proves). This file covers the two index rules a new migration has to satisfy
by construction, not by a later measurement, because both failure modes are
invisible at seed scale and degrade on their own as the affected table grows.
`DATABASE_AUTHORITY.md#a-retention-sweeps-predicate-must-match-a-usable-index-and-its-cascade-children-must-carry-one`
has the full detail and the measurements behind them (#1253,
migration `48_retention_index_gaps`); this is the short form to check against
before a migration lands.

- **A retention sweep's `WHERE` must name a column a usable index leads
  with.** A partial index's own predicate does not count toward that unless
  the sweep repeats it — an index built `WHERE consumed_at IS NULL` serves
  "find live rows," not "find rows old enough to delete," and the planner
  falls back to a sequential scan no matter how selective the sweep's
  predicate is on its own.
- **Every column an `ON DELETE CASCADE` can reach from a bulk-deleted parent
  needs its own index.** The cascade trigger fires once per deleted parent
  row; with no index on the child's FK column it sequentially scans the whole
  child table each time, so the cost is `rows_deleted x child_table_size` and
  gets worse as the child grows, unmeasured by a seed-scale test that happens
  to run before the child table is large.

A new migration that adds a retention predicate, a bulk `DELETE`/`UPDATE`
reachable from an admin action (role deletion, organization deletion), or a
new `ON DELETE CASCADE` on a column such a statement can reach adds the
matching index in the same migration — every object schema-qualified
`public.<name>`, DDL and DML alike (`ALTER TABLE public.x`, `CREATE INDEX …
ON public.x`, `DROP INDEX IF EXISTS public.x`, `INSERT INTO public.x`).
`1_baseline.up.sql` is `pg_dump` output and, near its top, sets the session's
`search_path` to empty with `set_config('search_path', '', false)` — not
transaction-local, so it holds for every migration golang-migrate applies
afterward in the same run, through the whole ledger, not only above some
baseline frontier. An unqualified reference fails `relation "..." does not
exist` (42P01) the moment it runs, in any replay of the ledger from a clean
database, including the one `migration-reference-gate.mjs --replay` runs
locally — it is not CI-only. `qualification/managed-database/run.py` enforces
the same rule a second, independent way, replaying migrations above the
managed-database baseline as a role whose own `search_path` does not resolve
`public` either.
