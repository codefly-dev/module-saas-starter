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
