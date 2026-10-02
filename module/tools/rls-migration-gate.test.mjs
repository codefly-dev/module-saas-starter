import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { analyzeSql, definerSearchPathErrors, rlsGateErrors } from "./rls-migration-gate.mjs";

const tenantSetup = `
CREATE TABLE t (id UUID PRIMARY KEY, org_id UUID NOT NULL);
GRANT SELECT, INSERT, UPDATE, DELETE ON t TO app_tenant;
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
    GRANT SELECT ON t TO app_tenant;
    ALTER TABLE t ENABLE ROW LEVEL SECURITY;
    CREATE POLICY p ON t USING (org_id::text = current_setting('app.current_org_id', true));
  `;
  const errors = analyzeSql(sql);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /missing FORCE ROW LEVEL SECURITY/);
});

test("a table with no RLS at all reports both ENABLE and FORCE missing", () => {
  const errors = analyzeSql("CREATE TABLE t (org_id UUID); GRANT SELECT ON t TO app_tenant;");
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
    GRANT SELECT ON a TO app_tenant;
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
    GRANT SELECT ON t TO app_tenant;
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

// A partitioned tenant table whose policies are complete; the partitions are what
// the cases below vary.
const partitionedTenant = `
CREATE TABLE t (id UUID NOT NULL, org_id UUID, created_at TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (created_at);
GRANT SELECT, INSERT ON t TO app_tenant;
ALTER TABLE t ENABLE ROW LEVEL SECURITY;
ALTER TABLE t FORCE ROW LEVEL SECURITY;
` + forAllPolicy;

const partitionFunction = (extra = "") => `
CREATE OR REPLACE FUNCTION t_ensure_partition(month date) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
    EXECUTE format('CREATE TABLE %I PARTITION OF t FOR VALUES FROM (%L) TO (%L)', 't_' || to_char(month, 'YYYY_MM'), month, month + 31);
    ${extra}
END;
$$;
`;

test("a function that creates tenant partitions without securing them is caught", () => {
  const errors = analyzeSql(partitionedTenant + partitionFunction());
  assert.equal(errors.length, 1);
  assert.match(errors[0], /^t: t_ensure_partition\(\) creates partitions without ENABLE \+ FORCE ROW LEVEL SECURITY/);
});

test("a function that enables, forces and creates policies on its partitions passes", () => {
  const secured = partitionFunction(`
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', 't_' || to_char(month, 'YYYY_MM'));
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', 't_' || to_char(month, 'YYYY_MM'));
    EXECUTE format('CREATE POLICY t_tenant ON %I USING (true)', 't_' || to_char(month, 'YYYY_MM'));`);
  assert.deepEqual(analyzeSql(partitionedTenant + secured), []);
});

test("securing only half of a partition does not count", () => {
  const halfSecured = partitionFunction(`
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', 't_' || to_char(month, 'YYYY_MM'));`);
  assert.equal(analyzeSql(partitionedTenant + halfSecured).length, 1);
});

test("a partition function may secure each partition through a helper it calls", () => {
  const helper = `
CREATE FUNCTION public.t_secure_partition(target regclass) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', target);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', target);
    EXECUTE format('CREATE POLICY t_tenant ON %s USING (true)', target);
END;
$$;
`;
  const sql = partitionedTenant + helper + partitionFunction("PERFORM public.t_secure_partition(to_regclass('t_x'));");
  assert.deepEqual(analyzeSql(sql), []);
  // Dropping the helper leaves the partition function calling nothing that secures.
  assert.equal(analyzeSql(sql + "\nDROP FUNCTION public.t_secure_partition(regclass);").length, 1);
});

test("a later migration that redefines the partition function to secure it clears the finding", () => {
  const redefined = partitionFunction(`
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', 't_' || to_char(month, 'YYYY_MM'));
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', 't_' || to_char(month, 'YYYY_MM'));
    EXECUTE format('CREATE POLICY t_tenant ON %I USING (true)', 't_' || to_char(month, 'YYYY_MM'));`);
  assert.deepEqual(analyzeSql(partitionedTenant + partitionFunction() + redefined), []);
});

test("a partition declared in a migration owes its own row level security", () => {
  const declared = "CREATE TABLE t_2026_01 PARTITION OF t FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');";
  const errors = analyzeSql(partitionedTenant + declared);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /^t_2026_01: tenant-scoped \(org_id\) but missing ENABLE \+ FORCE ROW LEVEL SECURITY/);

  const secured = `${declared}
ALTER TABLE t_2026_01 ENABLE ROW LEVEL SECURITY;
ALTER TABLE t_2026_01 FORCE ROW LEVEL SECURITY;
CREATE POLICY t_tenant ON t_2026_01
    USING (org_id::text = current_setting('app.current_org_id', true))
    WITH CHECK (org_id::text = current_setting('app.current_org_id', true));`;
  assert.deepEqual(analyzeSql(partitionedTenant + secured), []);
});

test("partitions of a table request traffic cannot reach owe no policy", () => {
  const unreachable = partitionedTenant.replace("GRANT SELECT, INSERT ON t TO app_tenant;", "");
  assert.deepEqual(analyzeSql(unreachable + partitionFunction()), []);
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


test("a private control-plane relation may deny every tenant row", () => {
  assert.deepEqual(analyzeSql(tenantSetup + `CREATE POLICY t_private ON t FOR ALL USING (false) WITH CHECK (false);`), []);
});

test("deny-all does not make an append-only trigger reachable", () => {
  const errors = analyzeSql(tenantSetup + `CREATE POLICY t_private ON t FOR ALL USING (false) WITH CHECK (false);` + appendOnlyTrigger);
  assert.equal(errors.length, 2);
  assert.ok(errors.every(e => e.includes("no RLS policy admits")));
});

test("a false literal inside an unconditional expression is not deny-all", () => {
  const errors = analyzeSql(tenantSetup + `CREATE POLICY t_private ON t FOR ALL USING (false OR true) WITH CHECK (true);`);
  assert.equal(errors.length, 2);
});


// pg_dump spells the same schema differently: `ALTER TABLE ONLY`, an upper-case
// CURRENT_USER, a `::name` cast and doubled parentheses. A baseline exported from
// a database is read with the same rules as the SQL that produced it.
test("pg_dump's spelling of forced RLS and the exact-role policy is the same schema", () => {
  const dumped = `
CREATE TABLE public.t (id uuid NOT NULL, org_id uuid NOT NULL);
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE public.t TO app_tenant;
ALTER TABLE public.t ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY public.t FORCE ROW LEVEL SECURITY;
CREATE POLICY t_tenant ON public.t USING (((org_id)::text = current_setting('app.current_org_id'::text, true))) WITH CHECK (((org_id)::text = current_setting('app.current_org_id'::text, true)));
CREATE POLICY app_job_worker_explicit_rows ON public.t TO app_job_worker USING ((CURRENT_USER = 'app_job_worker'::name)) WITH CHECK ((CURRENT_USER = 'app_job_worker'::name));
`;
  assert.deepEqual(analyzeSql(dumped), []);
  const wrongRole = dumped.replace("(CURRENT_USER = 'app_job_worker'::name)) WITH", "(CURRENT_USER = 'app_tenant'::name)) WITH");
  assert.equal(analyzeSql(wrongRole).length, 1);
});

// A worker or platform relation can carry an org_id column and still owe no
// policy: the request role is granted nothing on it, so the grant is the boundary.
// The moment request traffic is granted the table, isolation is owed again — and
// a schema-wide grant reaches every table at once.
test("a tenant-column table the request role cannot reach owes no policy", () => {
  const worker = `
CREATE TABLE public.w (id uuid NOT NULL, org_id uuid NOT NULL);
GRANT SELECT ON TABLE public.w TO app_job_worker;
GRANT SELECT,INSERT,UPDATE ON TABLE public.w TO app_control_plane;
`;
  assert.deepEqual(analyzeSql(worker), []);
  assert.equal(analyzeSql(worker + "GRANT SELECT ON public.w TO app_tenant;").length, 1);
  assert.equal(analyzeSql(worker + "GRANT SELECT ON ALL TABLES IN SCHEMA public TO app_tenant;").length, 1);
});

test("exact named background role policies are scoped by current SQL identity", () => {
  for (const role of ["app_control_plane", "app_billing_worker", "app_webhook_worker", "app_job_worker"]) {
    assert.deepEqual(analyzeSql(tenantSetup + `CREATE POLICY p ON t FOR ALL TO ${role} USING (current_user = '${role}') WITH CHECK (current_user = '${role}');`), []);
  }
});

test("background policy exception refuses public, mismatched roles, flags and extra clauses", () => {
  for (const [role, expression] of [
    ["public", "current_user = 'app_control_plane'"],
    ["app_tenant", "current_user = 'app_tenant'"],
    ["app_job_worker", "current_user = 'app_control_plane'"],
    ["app_control_plane, app_tenant", "current_user = 'app_control_plane'"],
    ["app_control_plane", "current_user = 'app_control_plane' OR true"],
    ["app_control_plane", "current_setting('app.role') = 'app_control_plane'"],
  ]) {
    assert.ok(analyzeSql(tenantSetup + `CREATE POLICY p ON t TO ${role} USING (${expression});`).length > 0);
  }
  assert.ok(analyzeSql(tenantSetup + `CREATE POLICY p ON t TO app_control_plane USING (current_user = 'app_control_plane'); ALTER POLICY p ON t TO public;`).length > 0);
});

// SECURITY DEFINER functions list pg_temp last in their search_path. The
// definitions below are spelled the way pg_dump writes the baseline, and the
// way a hand-written migration usually is.
const definer = (path = `SET search_path TO 'pg_catalog', 'public', 'pg_temp'`) => `
CREATE FUNCTION public.bump(target_org uuid, stamp timestamp with time zone) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    ${path}
    AS $$
BEGIN
    UPDATE organizations SET updated_at = stamp WHERE id = target_org;
END;
$$;
`;

test("a SECURITY DEFINER function that lists pg_temp last passes", () => {
  assert.deepEqual(definerSearchPathErrors(definer()), []);
  assert.deepEqual(definerSearchPathErrors(definer("SET search_path = public, pg_temp")), []);
  assert.deepEqual(definerSearchPathErrors(definer(`SET search_path TO "$user", public, pg_temp`)), []);
});

test("a SECURITY DEFINER function whose search_path omits pg_temp is refused", () => {
  const errors = definerSearchPathErrors(definer(`SET search_path TO 'pg_catalog', 'public'`));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /^bump\(uuid, timestamp with time zone\): SECURITY DEFINER, but its search_path is pg_catalog, public/);
});

test("a SECURITY DEFINER function with no search_path, or pg_temp anywhere but last, is refused", () => {
  assert.match(definerSearchPathErrors(definer(""))[0], /search_path is not set/);
  assert.equal(definerSearchPathErrors(definer("SET search_path = pg_temp, pg_catalog, public")).length, 1);
  assert.equal(definerSearchPathErrors(definer("SET search_path = pg_catalog, pg_temp, public")).length, 1);
  // A quoted "PG_TEMP" is some other schema.
  assert.equal(definerSearchPathErrors(definer(`SET search_path = pg_catalog, public, "PG_TEMP"`)).length, 1);
  assert.match(definerSearchPathErrors(definer("SET search_path FROM CURRENT"))[0], /FROM CURRENT/);
});

test("clauses after the body count, and text inside the body does not", () => {
  const trailing = `
CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;`;
  assert.deepEqual(definerSearchPathErrors(trailing), []);
  const mentioned = `
CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    -- SECURITY DEFINER
    RAISE NOTICE 'SET search_path = public';
END;
$$;`;
  assert.deepEqual(definerSearchPathErrors(mentioned), []);
  const quotedBody = `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1' SECURITY DEFINER;`;
  assert.equal(definerSearchPathErrors(quotedBody).length, 1);
});

test("a SECURITY INVOKER function owes no pinned search_path", () => {
  assert.deepEqual(definerSearchPathErrors(definer("").replace("SECURITY DEFINER", "")), []);
  assert.deepEqual(definerSearchPathErrors(definer("").replace("SECURITY DEFINER", "SECURITY INVOKER")), []);
});

test("a later ALTER FUNCTION that pins pg_temp last clears the finding, by any spelling of the signature", () => {
  const unpinned = definer(`SET search_path TO 'pg_catalog', 'public'`);
  for (const signature of ["public.bump(uuid,timestamp with time zone)", "bump(uuid, timestamptz)", "bump(target_org uuid, stamp timestamptz)", "bump"]) {
    const pinned = `ALTER FUNCTION ${signature} SET search_path TO 'pg_catalog', 'public', 'pg_temp';`;
    assert.deepEqual(definerSearchPathErrors(unpinned + pinned), [], signature);
  }
});

test("a later ALTER or CREATE OR REPLACE that drops pg_temp brings the finding back", () => {
  const pinned = definer();
  assert.equal(definerSearchPathErrors(pinned + "ALTER FUNCTION public.bump(uuid, timestamp with time zone) RESET search_path;").length, 1);
  assert.equal(definerSearchPathErrors(pinned + "ALTER FUNCTION public.bump(uuid, timestamp with time zone) RESET ALL;").length, 1);
  assert.equal(definerSearchPathErrors(pinned + "ALTER FUNCTION public.bump(uuid, timestamp with time zone) SET search_path TO DEFAULT;").length, 1);
  assert.equal(definerSearchPathErrors(pinned + "ALTER FUNCTION public.bump(uuid, timestamp with time zone) SET search_path = public;").length, 1);
  // CREATE OR REPLACE resets every clause it does not repeat.
  const replaced = definer("").replace("CREATE FUNCTION", "CREATE OR REPLACE FUNCTION");
  assert.equal(definerSearchPathErrors(pinned + replaced).length, 1);
});

test("an ALTER that makes a function SECURITY DEFINER holds it to the rule, and SECURITY INVOKER releases it", () => {
  const invoker = "CREATE FUNCTION public.g(p integer) RETURNS integer LANGUAGE sql SET search_path = public AS $$ SELECT p $$;";
  assert.deepEqual(definerSearchPathErrors(invoker), []);
  const promoted = invoker + "ALTER FUNCTION public.g(int) SECURITY DEFINER;";
  assert.equal(definerSearchPathErrors(promoted).length, 1);
  assert.deepEqual(definerSearchPathErrors(promoted + "ALTER FUNCTION public.g(int4) SET search_path = public, pg_temp;"), []);
  assert.deepEqual(definerSearchPathErrors(promoted + "ALTER FUNCTION public.g(integer) SECURITY INVOKER;"), []);
  // One ALTER may carry both actions.
  assert.deepEqual(definerSearchPathErrors(invoker + "ALTER FUNCTION public.g(integer) SECURITY DEFINER SET search_path = public, pg_temp;"), []);
});

test("an ALTER on one overload leaves the other one's finding standing", () => {
  const overloads = `
CREATE FUNCTION public.h(a uuid) RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path = public AS $$ SELECT $$;
CREATE FUNCTION public.h(a text) RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path = public AS $$ SELECT $$;
ALTER FUNCTION public.h(uuid) SET search_path = public, pg_temp;`;
  const errors = definerSearchPathErrors(overloads);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /^h\(text\):/);
});

test("a dropped or renamed function is tracked by the name it now has", () => {
  const unpinned = definer(`SET search_path TO 'pg_catalog', 'public'`);
  assert.deepEqual(definerSearchPathErrors(unpinned + "DROP FUNCTION IF EXISTS public.bump(uuid, timestamp with time zone) CASCADE;"), []);
  const renamed = definerSearchPathErrors(unpinned + "ALTER FUNCTION public.bump(uuid, timestamptz) RENAME TO bump_again;");
  assert.equal(renamed.length, 1);
  assert.match(renamed[0], /^bump_again\(/);
});

test("an ALTER that makes an unknown routine SECURITY DEFINER is refused", () => {
  const errors = definerSearchPathErrors("ALTER FUNCTION public.from_an_extension(uuid) SECURITY DEFINER;");
  assert.equal(errors.length, 1);
  assert.match(errors[0], /cannot resolve/);
});

test("procedures are held to the same rule", () => {
  const procedure = "CREATE PROCEDURE public.p(x uuid) LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN END $$;";
  assert.equal(definerSearchPathErrors(procedure).length, 1);
  assert.deepEqual(definerSearchPathErrors(procedure + "ALTER PROCEDURE public.p(uuid) SET search_path = public, pg_temp;"), []);
});

test("rlsGateErrors refuses a migration tree whose SECURITY DEFINER function omits pg_temp", () => {
  const root = mkdtempSync(join(tmpdir(), "rls-gate-"));
  const migrations = join(root, "services", "store", "migrations");
  try {
    mkdirSync(migrations, { recursive: true });
    writeFileSync(join(migrations, "1_create.up.sql"), definer(`SET search_path TO 'pg_catalog', 'public'`));
    assert.equal(rlsGateErrors(root).length, 1);
    writeFileSync(join(migrations, "2_pin.up.sql"), "ALTER FUNCTION public.bump(uuid, timestamp with time zone) SET search_path = pg_catalog, public, pg_temp;");
    assert.deepEqual(rlsGateErrors(root), []);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
