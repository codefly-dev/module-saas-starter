-- Close the compatibility window: every policy and routine body that reads both
-- scope settings goes back to reading the platform Postgres library's name
-- alone, in the spelling it had before 66 wrapped it.
--
-- This is the exact reverse of the up half, which is what makes it the text of
-- the later migration that closes the window for good — the one cut once no
-- deployable image binds the retired names. It is the same catalog-driven walk,
-- the same ALTER POLICY (so no policy's kind, verb or role list is restated),
-- and the same replace-as-owner idiom for a routine the baseline reassigned.
--
-- Running it as a rollback puts the database back where 65 left it, which is
-- also where the version skew it exists to survive bites: a pod that binds the
-- retired name reads no rows. Roll the image back too, or forward past it.

SET LOCAL lock_timeout = '15s';

CREATE FUNCTION pg_temp.scope_settings_single_read(body text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE AS $single$
DECLARE
    pair      record;
    live      text;
    legacy    text;
    compat_re text;
BEGIN
    FOR pair IN
        SELECT *
          FROM (VALUES ('codefly.current_tenant_id', 'app.current_org_id'),
                       ('codefly.current_user_id', 'app.current_user_id'))
               AS p(live, legacy)
    LOOP
        live := replace(pair.live, '.', '\.');
        legacy := replace(pair.legacy, '.', '\.');
        -- The compatibility read, with the qualification and the cast of its
        -- FIRST arm captured: the up half wrote both arms from one match, so
        -- restoring the first arm's spelling restores the text it replaced.
        compat_re := 'COALESCE\(NULLIF\(((?:pg_catalog\.)?)current_setting\(''' || live
                     || '''(::text)?, true\), ''''::text\), (?:pg_catalog\.)?current_setting\(''' || legacy
                     || '''(?:::text)?, true\)\)';
        body := regexp_replace(body, compat_re,
                               '\1current_setting(''' || pair.live || '''\2, true)', 'g');
    END LOOP;
    RETURN body;
END
$single$;

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
        new_qual := pg_temp.scope_settings_single_read(COALESCE(pol.qual, ''));
        new_check := pg_temp.scope_settings_single_read(COALESCE(pol.with_check, ''));
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
        new_def := pg_temp.scope_settings_single_read(routine.definition);
        IF new_def = routine.definition THEN
            CONTINUE;
        END IF;
        IF routine.owner = current_user THEN
            EXECUTE new_def;
        ELSE
            EXECUTE format('GRANT CREATE ON SCHEMA public TO %I', routine.owner);
            EXECUTE format('SET LOCAL ROLE %I', routine.owner);
            EXECUTE new_def;
            EXECUTE 'RESET ROLE';
            EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I', routine.owner);
        END IF;
        rewritten := rewritten + 1;
    END LOOP;

    IF rewritten = 0 AND NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_policy p
         WHERE COALESCE(pg_get_expr(p.polqual, p.polrelid), '')
               || COALESCE(pg_get_expr(p.polwithcheck, p.polrelid), '')
               ~ 'current_setting\(''codefly\.current_tenant_id'
    ) THEN
        RAISE EXCEPTION 'no policy or routine in this database reads either scope setting, so there is no compatibility window to close'
            USING HINT = 'check the connection: this is not a database whose tenant isolation reads codefly.current_tenant_id';
    END IF;
    RAISE NOTICE 'scope settings: % policies and routines now read the library name alone', rewritten;
END
$transition$;

DROP FUNCTION pg_temp.scope_settings_single_read(text);
