-- Open the compatibility window on the two scope settings: every policy and
-- routine body that reads one reads both, the platform Postgres library's name
-- first and the host's retired one second.
--
-- Migration 65 renamed app.current_org_id / app.current_user_id onto
-- codefly.current_tenant_id / codefly.current_user_id in one step, and a
-- one-step rename of an RLS setting is not deployable: the catalog and the
-- binary that binds it ship separately, and the half that loses the race reads
-- an unset setting, which is zero rows on every scoped SELECT and a WITH CHECK
-- failure on every INSERT, silently. This migration is the transition 65 needed
-- — paired with a host that binds both names, so a pod serves correctly against
-- a catalog from before the rename, after it, or in this window.
--
-- The body below is ../../../tools/rls-scope-settings-transition.sql, byte for
-- byte: a consumer whose own migrations wrote tenant policies against the
-- convention this repository documented before 65 runs that file against its
-- own database and lands in the same state, and
-- tools/rls_scope_settings_recipe_test.go holds the two texts in step. It is
-- catalog-driven rather than generated, so it also reaches a policy whose name
-- depends on the month (the partitions of public.audit_events) and a consumer's
-- own policies, which no generated file here could name.
--
-- The window closes in a later migration: the same catalog-driven walk,
-- rewriting the compatibility read back to a single read of the library's name.
-- That text is 66_scope_settings_transition.down.sql, which the replay test
-- already proves, renumbered above the frontier and cut once no deployable
-- image binds the retired names. It is a release boundary, not a cleanup, and
-- it must not ship beside this one -- together they are the one-step rename
-- again.

-- rls-scope-settings-transition — open the compatibility window on the two
-- scope settings, over whatever this database's policies and routines read now.
--
-- A tenant-scoped policy reads `codefly.current_tenant_id` and a user-scoped
-- one `codefly.current_user_id` — the defaults of the platform's Postgres
-- library, which installs them for every transaction opened through its scoped
-- factory. The host carried names of its own, `app.current_org_id` and
-- `app.current_user_id`, until store migration 65 renamed every policy and
-- routine body onto the library's two.
--
-- A rename is not deployable on its own. The policy and the binary that binds
-- it ship separately, and the predicate is `col::text = current_setting(<name>,
-- true)`: a binary that binds the other name reads NULL, so every scoped SELECT
-- returns zero rows with no error and every INSERT fails its WITH CHECK. A
-- rolling update applies the migration before the new pods are ready; a
-- rollback of the image leaves the whole fleet there indefinitely. Nothing
-- raises — the loss of access is silent and total.
--
-- This rewrites every such predicate to read BOTH names, the library's first:
--
--   COALESCE(NULLIF(current_setting('codefly.current_tenant_id', true), ''),
--            current_setting('app.current_org_id', true))
--
-- so one database serves a binary that binds either name, and a host that binds
-- both is correct against a catalog from before the rename, after it, or in this
-- window. Fail-closed is preserved exactly: a transaction that binds neither
-- half reads '' through both arms, the predicate is false, and the relation is
-- empty rather than open. An absent setting and an empty one stay the same
-- thing, which is what NULLIF is for.
--
-- It is catalog-driven on purpose. The whole set — 75 policies over 70
-- relations, 6 more on partitions of public.audit_events, and 9 routine bodies
-- when migration 65 renamed them — is known only to the catalog, and a
-- consumer's own set is known only to the consumer's. So this reads what is
-- there rather than naming it, and reaches the same end state from either side
-- of the rename:
--
--   * a predicate already reading both names is left exactly as it is, so the
--     rewrite is idempotent;
--   * a predicate reading the library's name is wrapped;
--   * a predicate still reading the host's retired name — a consumer that
--     wrote its policies against the convention this repository documented
--     before migration 65 — is wrapped to the same text, which is also that
--     consumer's rename.
--
-- A policy's kind, verb and role list are not reassigned: this is ALTER POLICY,
-- which cannot change them, rather than DROP and CREATE, which restates them
-- all and can get one wrong. A routine is replaced as its own owner, because
-- the baseline hands many of them to an application role (see the "Replacing a
-- function the baseline reassigned" idiom in
-- services/store/migrations/README.md); CREATE OR REPLACE keeps the owner and
-- the grants.
--
-- Running it by hand, against a consumer's own database:
--
--   psql "$DATABASE_URL" --single-transaction \
--        -f modules/<name>/tools/rls-scope-settings-transition.sql
--
-- It must run in a transaction — every statement below belongs to one change —
-- and as the principal that installs that database's migrations, which owns the
-- tables and the schema. It is also store migration 66 of this module's own
-- ledger, byte for byte, and a test holds the two in step.
--
-- The window closes in a later migration, which rewrites these predicates back
-- to a single read of the library's name once no deployable binary binds the
-- retired one. Until then a policy reading the retired name ALONE is still a
-- silent empty relation, which is what the host's startup check and
-- tools/rls-migration-gate.mjs refuse.

-- An exclusive lock on every scoped relation, taken in one transaction. Bound
-- the wait: unbounded, this queues behind one long-running query and then
-- blocks every statement on 70 relations for as long as that query runs, which
-- is an outage with a deploy's name on it. Bounded, it fails and is retried.
SET LOCAL lock_timeout = '15s';

-- The rewrite, as one text-to-text function: the same substitution has to reach
-- a policy's USING, its WITH CHECK, and a routine's whole definition, and the
-- three are separately executed statements.
CREATE FUNCTION pg_temp.scope_settings_compatibility(body text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE AS $compatibility$
DECLARE
    pair        record;
    live        text;   -- the library's name, dots escaped for the pattern
    legacy      text;   -- the host's retired name, likewise
    live_re     text;
    legacy_re   text;
    compat_re   text;
    compat      text;
BEGIN
    FOR pair IN
        SELECT *
          FROM (VALUES ('codefly.current_tenant_id', 'app.current_org_id'),
                       ('codefly.current_user_id', 'app.current_user_id'))
               AS p(live, legacy)
    LOOP
        live := replace(pair.live, '.', '\.');
        legacy := replace(pair.legacy, '.', '\.');
        -- One read of one setting, in both spellings that occur: optionally
        -- schema-qualified, with or without the ::text cast the parser adds and
        -- the deparser prints. \1 is the qualification and \2 the cast, so the
        -- rewrite keeps whichever the source had — that is what makes the
        -- window's own reversal exact, and it keeps a SECURITY DEFINER body's
        -- pg_catalog. qualification rather than quietly dropping it.
        live_re := '((?:pg_catalog\.)?)current_setting\(''' || live || '''(::text)?, true\)';
        legacy_re := '((?:pg_catalog\.)?)current_setting\(''' || legacy || '''(::text)?, true\)';
        compat := 'COALESCE(NULLIF(\1current_setting(''' || pair.live || '''\2, true), ''''::text), '
                  || '\1current_setting(''' || pair.legacy || '''\2, true))';
        compat_re := 'COALESCE\(NULLIF\((?:pg_catalog\.)?current_setting\(''' || live
                     || '''(?:::text)?, true\), ''''::text\), (?:pg_catalog\.)?current_setting\(''' || legacy
                     || '''(?:::text)?, true\)\)';

        IF body ~ compat_re THEN
            -- Already inside the window. Wrapping it again would nest a second
            -- COALESCE around the first and still read correctly, which is the
            -- worst kind of no-op: it would make replay produce a different
            -- catalog every time.
            CONTINUE;
        END IF;
        IF body ~ live_re THEN
            body := regexp_replace(body, live_re, compat, 'g');
            CONTINUE;
        END IF;
        IF body ~ legacy_re THEN
            -- Both patterns capture the qualification and the cast in the same
            -- two groups, so one replacement text serves either direction.
            body := regexp_replace(body, legacy_re, compat, 'g');
        END IF;
    END LOOP;
    RETURN body;
END
$compatibility$;

DO $transition$
DECLARE
    pol       record;
    routine   record;
    new_qual  text;
    new_check text;
    new_def   text;
    rewritten int := 0;
BEGIN
    FOR pol IN
        SELECT p.polname,
               p.polrelid::regclass AS relation,
               pg_get_expr(p.polqual, p.polrelid) AS qual,
               pg_get_expr(p.polwithcheck, p.polrelid) AS with_check
          FROM pg_catalog.pg_policy p
          JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public'
         ORDER BY p.polrelid::regclass::text, p.polname
    LOOP
        -- Every partition of a partitioned table carries its own copy of the
        -- parent's policies, and a query that names the partition is judged by
        -- those alone. They are rows of pg_policy like any other, so this loop
        -- reaches them without knowing a partition's name — which depends on
        -- the month.
        new_qual := pg_temp.scope_settings_compatibility(COALESCE(pol.qual, ''));
        new_check := pg_temp.scope_settings_compatibility(COALESCE(pol.with_check, ''));
        IF new_qual = COALESCE(pol.qual, '') AND new_check = COALESCE(pol.with_check, '') THEN
            CONTINUE;
        END IF;
        IF pol.qual IS NOT NULL AND pol.with_check IS NOT NULL THEN
            EXECUTE format('ALTER POLICY %I ON %s USING (%s) WITH CHECK (%s)',
                           pol.polname, pol.relation, new_qual, new_check);
        ELSIF pol.qual IS NOT NULL THEN
            EXECUTE format('ALTER POLICY %I ON %s USING (%s)', pol.polname, pol.relation, new_qual);
        ELSE
            EXECUTE format('ALTER POLICY %I ON %s WITH CHECK (%s)', pol.polname, pol.relation, new_check);
        END IF;
        rewritten := rewritten + 1;
    END LOOP;

    FOR routine IN
        SELECT pg_get_functiondef(p.oid) AS definition,
               pg_get_userbyid(p.proowner) AS owner,
               p.oid::regprocedure::text AS signature
          FROM pg_catalog.pg_proc p
          JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
         WHERE n.nspname = 'public' AND p.prokind IN ('f', 'p')
         ORDER BY p.oid::regprocedure::text
    LOOP
        new_def := pg_temp.scope_settings_compatibility(routine.definition);
        IF new_def = routine.definition THEN
            CONTINUE;
        END IF;
        IF routine.owner = current_user THEN
            EXECUTE new_def;
        ELSE
            -- The owner holds CREATE on the schema for this statement only.
            EXECUTE format('GRANT CREATE ON SCHEMA public TO %I', routine.owner);
            EXECUTE format('SET LOCAL ROLE %I', routine.owner);
            EXECUTE new_def;
            EXECUTE 'RESET ROLE';
            EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I', routine.owner);
        END IF;
        rewritten := rewritten + 1;
    END LOOP;

    -- A rewrite that found nothing is indistinguishable from a clean one, and
    -- the thing it is most likely to mean is that this is not the database
    -- anyone meant: the wrong connection, an empty schema, a ledger that never
    -- installed. Re-running it on a database already inside the window is
    -- legitimate and stays quiet.
    IF rewritten = 0 AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_policy p
         WHERE COALESCE(pg_get_expr(p.polqual, p.polrelid), '')
               || COALESCE(pg_get_expr(p.polwithcheck, p.polrelid), '')
               ~ 'current_setting\(''codefly\.current_tenant_id'
    ) THEN
        RAISE EXCEPTION 'no policy or routine in this database reads either scope setting, so there is nothing to put in the compatibility window'
            USING HINT = 'check the connection: this is not a database whose tenant isolation reads codefly.current_tenant_id or app.current_org_id';
    END IF;
    RAISE NOTICE 'scope settings: % policies and routines now read the library name and the retired one', rewritten;
END
$transition$;

DROP FUNCTION pg_temp.scope_settings_compatibility(text);
