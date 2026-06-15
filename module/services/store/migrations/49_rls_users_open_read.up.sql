-- Supersede 48's restrictive users policy: users is a SHARED identity table read
-- by id pervasively (login, co-member display, joins) and across orgs, so
-- row-level READ isolation fights legitimate reads and silently breaks untested
-- internal GetUser paths. Protect the integrity boundary that matters — WRITES —
-- and leave SELECT open. (See SAAS-RLS-IDENTITY-DESIGN.md.)

DROP POLICY IF EXISTS users_access ON users;

CREATE POLICY users_select ON users FOR SELECT USING (true);

CREATE POLICY users_insert ON users FOR INSERT
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );

CREATE POLICY users_update ON users FOR UPDATE
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    )
    WITH CHECK (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );

CREATE POLICY users_delete ON users FOR DELETE
    USING (
        current_setting('app.bypass', true) = '1'
        OR uuid::text = current_setting('app.current_user_id', true)
    );
