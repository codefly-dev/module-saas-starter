import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { analyzeSql, rlsGateErrors } from "./rls-migration-gate.mjs";

const tenantSetup = `
CREATE TABLE t (id UUID PRIMARY KEY, org_id UUID NOT NULL);
ALTER TABLE t ENABLE ROW LEVEL SECURITY;
ALTER TABLE t FORCE ROW LEVEL SECURITY;
`;

const forAllPolicy = `
CREATE POLICY t_tenant ON t
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
`;

const appendOnlyTrigger = `
CREATE OR REPLACE FUNCTION reject_durable_mutation() RETURNS TRIGGER AS $$
BEGIN RAISE EXCEPTION 'append-only'; END; $$ LANGUAGE plpgsql;
CREATE TRIGGER t_append_only BEFORE UPDATE OR DELETE ON t
    FOR EACH ROW EXECUTE FUNCTION reject_durable_mutation();
`;

test("a FORCE-RLS table with a FOR ALL tenant policy passes", () => {
  assert.deepEqual(analyzeSql(tenantSetup + forAllPolicy), []);
});

test("an append-only trigger with only SELECT/INSERT policies is unreachable on UPDATE and DELETE", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_sel ON t FOR SELECT
        USING (org_id::text = current_setting('app.current_org_id', true));
    CREATE POLICY t_ins ON t FOR INSERT
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    ` +
    appendOnlyTrigger;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 2);
  assert.match(errors[0], /rejects DELETE but no RLS policy admits DELETE/);
  assert.match(errors[1], /rejects UPDATE but no RLS policy admits UPDATE/);
});

test("a FOR ALL policy keeps the append-only trigger reachable", () => {
  assert.deepEqual(analyzeSql(tenantSetup + forAllPolicy + appendOnlyTrigger), []);
});

test("explicit per-verb policies covering all four verbs keep the trigger reachable", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_sel ON t FOR SELECT
        USING (org_id::text = current_setting('app.current_org_id', true));
    CREATE POLICY t_ins ON t FOR INSERT
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    CREATE POLICY t_upd ON t FOR UPDATE
        USING (org_id::text = current_setting('app.current_org_id', true))
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    CREATE POLICY t_del ON t FOR DELETE
        USING (org_id::text = current_setting('app.current_org_id', true));
    ` +
    appendOnlyTrigger;
  assert.deepEqual(analyzeSql(sql), []);
});

test("a state-machine trigger that returns NEW is not treated as append-only", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_ins ON t FOR INSERT
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    CREATE FUNCTION enforce_state() RETURNS TRIGGER AS $$
    BEGIN
        IF NEW.org_id IS NULL THEN RAISE EXCEPTION 'bad'; END IF;
        RETURN NEW;
    END; $$ LANGUAGE plpgsql;
    CREATE TRIGGER t_state BEFORE INSERT OR UPDATE ON t
        FOR EACH ROW EXECUTE FUNCTION enforce_state();
    `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("forced RLS is required, not just enabled", () => {
  const sql = `
    CREATE TABLE t (org_id UUID);
    ALTER TABLE t ENABLE ROW LEVEL SECURITY;
    CREATE POLICY p ON t USING (org_id::text = current_setting('app.current_org_id', true));
  `;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /missing FORCE ROW LEVEL SECURITY/);
});

test("a table with no RLS at all reports both ENABLE and FORCE missing", () => {
  const errors = analyzeSql("CREATE TABLE t (org_id UUID);");
  assert.equal(errors.length, 1);
  assert.match(errors[0], /missing ENABLE \+ FORCE ROW LEVEL SECURITY/);
});

test("an unconditional policy predicate is rejected", () => {
  const sql =
    tenantSetup +
    "CREATE POLICY p ON t USING (true) WITH CHECK (true);";
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 2);
  assert.ok(errors.every((e) => /may be accidentally unconditional/.test(e)));
});

test("a user-scoped predicate counts as tenant-scoped", () => {
  const sql = `
    CREATE TABLE t (org_id UUID, user_id UUID);
    ALTER TABLE t ENABLE ROW LEVEL SECURITY;
    ALTER TABLE t FORCE ROW LEVEL SECURITY;
    CREATE POLICY p ON t
        USING (user_id::text = current_setting('app.current_user_id', true))
        WITH CHECK (user_id::text = current_setting('app.current_user_id', true));
  `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("tables without a tenant column are out of scope", () => {
  const sql = `
    CREATE TABLE t (id UUID, user_id UUID);
    CREATE POLICY t_open ON t FOR SELECT USING (true);
    CREATE FUNCTION f() RETURNS TRIGGER AS $$ BEGIN RAISE EXCEPTION 'x'; END; $$ LANGUAGE plpgsql;
    CREATE TRIGGER tr BEFORE DELETE ON t FOR EACH ROW EXECUTE FUNCTION f();
  `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("a nullable-org polymorphic FOR ALL policy passes", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_poly ON t
        USING (
            current_setting('app.bypass', true) = '1'
            OR (org_id IS NOT NULL AND org_id::text = current_setting('app.current_org_id', true))
        )
        WITH CHECK (
            current_setting('app.bypass', true) = '1'
            OR (org_id IS NOT NULL AND org_id::text = current_setting('app.current_org_id', true))
        );
    `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("expands DO/FOREACH/format loops that apply one recipe to many tables", () => {
  const sql = `
    CREATE TABLE a (org_id UUID);
    CREATE TABLE b (org_id UUID);
    DO $$
    DECLARE t TEXT;
    BEGIN
        FOREACH t IN ARRAY ARRAY['a', 'b'] LOOP
            EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
            EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
            EXECUTE format($f$
                CREATE POLICY %I_tenant ON %I
                    USING (org_id::text = current_setting('app.current_org_id', true))
                    WITH CHECK (org_id::text = current_setting('app.current_org_id', true))
            $f$, t, t);
        END LOOP;
    END $$;
  `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("a DO loop that forgets FORCE is still caught after expansion", () => {
  const sql = `
    CREATE TABLE a (org_id UUID);
    DO $$
    DECLARE t TEXT;
    BEGIN
        FOREACH t IN ARRAY ARRAY['a'] LOOP
            EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
            EXECUTE format($f$
                CREATE POLICY %I_tenant ON %I
                    USING (org_id::text = current_setting('app.current_org_id', true))
            $f$, t, t);
        END LOOP;
    END $$;
  `;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /^a: .*missing FORCE/);
});

test("ALTER POLICY that rewrites a predicate to an unconditional one is caught", () => {
  const sql =
    tenantSetup +
    forAllPolicy +
    "ALTER POLICY t_tenant ON t USING (true) WITH CHECK (true);";
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 2);
  assert.ok(errors.every((e) => /accidentally unconditional/.test(e)));
});

test("DROP TABLE removes a table from the tenant set", () => {
  const sql = "CREATE TABLE t (org_id UUID); DROP TABLE t;";
  assert.deepEqual(analyzeSql(sql), []);
});

test("a JOIN ... USING (col) inside a predicate is not mistaken for the policy's USING clause", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_ins ON t FOR INSERT
        WITH CHECK (EXISTS (
            SELECT 1 FROM parent p JOIN grandparent g USING (gid)
            WHERE p.id = t.org_id
              AND g.org_id::text = current_setting('app.current_org_id', true)
        ));
    `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("a restrictive policy with an orthogonal predicate is not flagged as unconditional", () => {
  const sql =
    tenantSetup +
    forAllPolicy +
    "CREATE POLICY t_soft ON t AS RESTRICTIVE FOR ALL USING (archived_at IS NULL);";
  assert.deepEqual(analyzeSql(sql), []);
});

test("a restrictive-only policy does not admit an append-only-guarded verb", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY t_r ON t AS RESTRICTIVE
        USING (org_id::text = current_setting('app.current_org_id', true))
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    ` +
    appendOnlyTrigger;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 2);
  assert.ok(errors.every((e) => /no RLS policy admits/.test(e)));
});

test("a DO block using plain EXECUTE literals to force RLS is recognized", () => {
  const sql = `
    CREATE TABLE t (org_id UUID);
    DO $$ BEGIN
        EXECUTE 'ALTER TABLE t ENABLE ROW LEVEL SECURITY';
        EXECUTE 'ALTER TABLE t FORCE ROW LEVEL SECURITY';
        EXECUTE 'CREATE POLICY p ON t USING (org_id::text = current_setting(''app.current_org_id'', true)) WITH CHECK (org_id::text = current_setting(''app.current_org_id'', true))';
    END $$;
  `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("a DO block whose EXECUTE literals forget FORCE is still caught", () => {
  const sql = `
    CREATE TABLE t (org_id UUID);
    DO $$ BEGIN
        EXECUTE 'ALTER TABLE t ENABLE ROW LEVEL SECURITY';
        EXECUTE 'CREATE POLICY p ON t USING (org_id::text = current_setting(''app.current_org_id'', true))';
    END $$;
  `;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /missing FORCE/);
});

test("doubled single quotes inside string literals do not break scanning", () => {
  const sql = `
    CREATE TABLE t (note TEXT DEFAULT 'it''s; ok)', org_id UUID);
    ALTER TABLE t ENABLE ROW LEVEL SECURITY; ALTER TABLE t FORCE ROW LEVEL SECURITY;
    CREATE POLICY p ON t
        USING (note <> 'a'')b' AND org_id::text = current_setting('app.current_org_id', true))
        WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
  `;
  assert.deepEqual(analyzeSql(sql), []);
});

test("an append-only trigger on UPDATE OF a quoted column binds to the table, not the column", () => {
  const sql =
    tenantSetup +
    `
    CREATE POLICY s ON t FOR SELECT USING (org_id::text = current_setting('app.current_org_id', true));
    CREATE POLICY i ON t FOR INSERT WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
    CREATE FUNCTION reject() RETURNS TRIGGER AS $$ BEGIN RAISE EXCEPTION 'no'; END; $$ LANGUAGE plpgsql;
    CREATE TRIGGER t_ao BEFORE UPDATE OF "on" ON t FOR EACH ROW EXECUTE FUNCTION reject();
    `;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /rejects UPDATE but no RLS policy admits UPDATE/);
});

test("files without a numeric migration version are ignored", () => {
  const root = mkdtempSync(join(tmpdir(), "rls-gate-"));
  const migrations = join(root, "services", "store", "migrations");
  try {
    mkdirSync(migrations, { recursive: true });
    writeFileSync(join(migrations, "1_ok.up.sql"), "CREATE TABLE a (id UUID);");
    // A scratch file that is not a real migration: an unprotected tenant table that
    // golang-migrate would never apply, so the gate must not analyze it either.
    writeFileSync(join(migrations, "scratch.up.sql"), "CREATE TABLE b (org_id UUID);");
    assert.deepEqual(rlsGateErrors(root), []);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the shipped store migrations satisfy the gate", () => {
  assert.deepEqual(rlsGateErrors(join(import.meta.dirname, "..")), []);
});

test("rlsGateErrors returns nothing when the store service is not composed", () => {
  const root = mkdtempSync(join(tmpdir(), "rls-gate-"));
  try {
    mkdirSync(join(root, "services"), { recursive: true });
    assert.deepEqual(rlsGateErrors(root), []);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("rlsGateErrors reads a real migration tree in version order", () => {
  const root = mkdtempSync(join(tmpdir(), "rls-gate-"));
  const migrations = join(root, "services", "store", "migrations");
  try {
    mkdirSync(migrations, { recursive: true });
    writeFileSync(join(migrations, "1_create.up.sql"), "CREATE TABLE t (org_id UUID);");
    writeFileSync(
      join(migrations, "2_rls.up.sql"),
      "ALTER TABLE t ENABLE ROW LEVEL SECURITY; ALTER TABLE t FORCE ROW LEVEL SECURITY;\n" +
        "CREATE POLICY p ON t USING (org_id::text = current_setting('app.current_org_id', true)) " +
        "WITH CHECK (org_id::text = current_setting('app.current_org_id', true));",
    );
    assert.deepEqual(rlsGateErrors(root), []);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
