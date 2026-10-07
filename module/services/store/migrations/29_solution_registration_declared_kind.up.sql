-- The registry records WHAT a declaration declared the presence of (issue #952).
--
-- Core requires `kind` on every presence document and refuses any other value
-- (`solutionhost.Kind`: exactly "solution" or "module"), so every admitted
-- declaration has one. The host dropped it on reconcile, and a registry that
-- cannot say which kind a record is cannot route modules at all: a solution is
-- served at /solutions/<alias>/* behind per-viewer installation admission, a
-- module at /v1/<alias>/* through the ordinary authenticated pipeline. A host
-- that could not tell them apart had to serve both from one surface — which puts
-- a solution's upstream behind no installation check — or serve neither, which
-- is what this host did.
--
-- The kind is NOT inferable from anything already stored. `publisher` is
-- attribution, `declared_release` is a release identity, and an alias is a path
-- segment; reading a kind off any of them is guessing at exactly the question
-- that separates a surface with per-viewer admission from one without.
ALTER TABLE public.solution_registrations
    ADD COLUMN declared_kind text;

-- Backfill, and why it is `solution` for every pre-existing declared row.
--
-- No DEPLOYED database has a declared row: the `declared_*` columns arrive in
-- migration 20 of this same unreleased ledger, so nothing before it ever ran the
-- declaration reconciler. What this covers is a database that applied 20..28 —
-- a developer's, a CI replay's — where a declared row may exist.
--
-- Such a row is being served at /solutions/<alias>/* RIGHT NOW, because that is
-- the only surface this host had. Writing `solution` therefore records what the
-- host is doing with the row rather than guessing what the document said, and it
-- is safe in the one direction that matters: it can never promote a record onto
-- the module surface. A module binding on such a database keeps being served at
-- the solution surface until its next generation applies, which rewrites the
-- record from the document and corrects the kind.
--
-- The alternative — migration 23's "clear the declaration whole" — is wrong
-- here. Clearing makes the row undeclared, which
-- `solution_registrations_undeclared_is_withdrawn` then requires to be
-- tombstoned, and nothing would bring it back: core answers DecisionCurrent for
-- an unchanged generation, so the reconciler would never rewrite the record it
-- had just emptied.
UPDATE public.solution_registrations
   SET declared_kind = 'solution'
 WHERE declared_binding_id IS NOT NULL
   AND declared_kind IS NULL;

-- Whole-or-absent, extended from four columns to five. Migration 20 defined this
-- as three, migration 23 as four; the declaration is one fact and a row carrying
-- part of it is a row no reader can act on.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_declared_whole;

ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_whole
        CHECK ((num_nonnulls(declared_binding_id, declared_generation, declared_release, declared_target_id, declared_kind) = ANY (ARRAY[0, 5])));

-- And the value is one of the two core admits. An enum would be a second place
-- to migrate for every future kind; a CHECK on a text column refuses the same
-- set and names it in the error.
ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_kind_check
        CHECK ((declared_kind IS NULL) OR (declared_kind = ANY (ARRAY['solution'::text, 'module'::text])));
