#!/usr/bin/env node
// rls-migration-gate — a database-free static gate over store migration SQL.
//
// The starter ships the role model, the shared store, and the RLS conventions,
// but a consumer owns its own migrations. Nothing otherwise verifies that a
// consumer's tenant-scoped tables are actually protected: a table can enable
// forced RLS, add a partial set of policies, and ship isolation that looks
// correct and is not. Under forced RLS a statement with no matching policy
// matches ZERO rows — so a DELETE "succeeds" having changed nothing, and an
// append-only trigger meant to reject that DELETE never fires. The bug appears
// exactly where someone is more explicit (per-verb policies) than the FOR ALL
// convention, which is the wrong way round.
//
// This replays every up-migration in version order over a small in-memory model
// (tables, forced-RLS state, policies, triggers, functions) and, for every table
// carrying a tenant column, asserts three invariants. See rlsGateErrors below.
// Partitions of a tenant table are held to them too: PostgreSQL applies a
// partitioned table's policies only to a query that names the parent, so a
// partition queried by name is checked against its own policies alone. A
// partition declared in a migration is modelled as a table of its own, and a
// function that creates partitions at runtime must secure each one it creates.
// It also holds every SECURITY DEFINER function to a search_path that lists
// pg_temp last (definerSearchPathErrors below): such a function runs with an
// owner that bypasses the tenant policies, and without that a caller's temporary
// table can stand in for a relation the function reads. Failing closed is
// appropriate — a silently unenforced isolation boundary is worse than a build
// error.
//
//   node tools/rls-migration-gate.mjs check   # fail on any unprotected tenant boundary
//
// The module root is the parent of tools/, so this works identically in
// canonical's `module/` and a consumer's `modules/<name>/`.

import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

// Columns that mark a table as belonging to a tenant (an organization). A table
// carrying one of these is required to isolate rows by tenant. User-scoped tables
// (keyed by user_id) and the self-referential organizations table are handled by
// their own conventions and are intentionally out of scope here.
const TENANT_COLUMNS = new Set(["org_id", "organization_id", "tenant_id"]);

// A predicate is tenant-scoped iff it reads one of the request-scoped settings the
// api sets per transaction (org for tenant isolation, user for the symmetric
// user-scoped tables). Anything else (notably a bare `true`) is unconditional.
const SCOPING_SETTING = /current_setting\(\s*'app\.current_(?:org|user|tenant)_id'/;

const MUTATION_VERBS = ["UPDATE", "DELETE"];

// A single trigger event: one of the four DML events, with UPDATE's optional column
// list. Constraining the event list (rather than matching ` ON ` greedily) keeps a
// column named like a keyword inside `UPDATE OF <col>` from being read as the table.
const TRIGGER_EVENT = String.raw`(?:INSERT|DELETE|TRUNCATE|UPDATE(?:\s+OF\s+[\w",\s]+?)?)`;
const TRIGGER_RE = new RegExp(
  String.raw`^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+"?\w+"?\s+` +
    String.raw`(?:BEFORE|AFTER|INSTEAD\s+OF)\s+` +
    String.raw`(${TRIGGER_EVENT}(?:\s+OR\s+${TRIGGER_EVENT})*)\s+ON\s+("?[\w.]+"?)` +
    String.raw`[\s\S]*?EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+("?[\w.]+"?)`,
  "i",
);

const stripName = (raw) =>
  raw.trim().replace(/^public\./i, "").replace(/"/g, "").toLowerCase();

// Index of the quote that closes the single-quoted string opened at `open`,
// treating a doubled '' as an escaped quote rather than a terminator (SQL string
// escaping). Returns -1 when the string is never closed.
function endOfString(sql, open) {
  for (let i = open + 1; i < sql.length; i++) {
    if (sql[i] !== "'") continue;
    if (sql[i + 1] === "'") {
      i++;
      continue;
    }
    return i;
  }
  return -1;
}

// Walk `sql` from `open` (the index of an opening paren) to its matching close,
// ignoring parens inside single-quoted or dollar-quoted regions. Returns the
// inner text and the index just past the closing paren.
function balanced(sql, open) {
  let depth = 0;
  for (let i = open; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") {
      i = endOfString(sql, i);
      if (i < 0) break;
      continue;
    }
    if (ch === "$") {
      const tag = /^\$\w*\$/.exec(sql.slice(i));
      if (tag) {
        const end = sql.indexOf(tag[0], i + tag[0].length);
        if (end < 0) break;
        i = end + tag[0].length - 1;
        continue;
      }
    }
    if (ch === "(") depth++;
    else if (ch === ")" && --depth === 0) {
      return { inner: sql.slice(open + 1, i), end: i + 1 };
    }
  }
  return { inner: "", end: sql.length };
}

// The parenthesised expression of a top-level policy clause. `keyword` matches the
// clause introducer immediately followed by its opening paren (e.g. /^USING\s*\(/).
// The introducer is honoured only at paren depth 0, so a `USING (` from a JOIN or a
// `CHECK` inside a subquery predicate is never mistaken for the policy's own clause.
// Returns null when the clause is absent.
function clauseExpr(rest, keyword) {
  let depth = 0;
  for (let i = 0; i < rest.length; i++) {
    const ch = rest[i];
    if (ch === "'") {
      i = endOfString(rest, i);
      if (i < 0) break;
      continue;
    }
    if (ch === "$") {
      const tag = /^\$\w*\$/.exec(rest.slice(i));
      if (tag) {
        const end = rest.indexOf(tag[0], i + tag[0].length);
        if (end < 0) break;
        i = end + tag[0].length - 1;
        continue;
      }
    }
    if (ch === "(") {
      depth++;
      continue;
    }
    if (ch === ")") {
      depth--;
      continue;
    }
    if (depth !== 0 || (i > 0 && /\w/.test(rest[i - 1]))) continue;
    const m = keyword.exec(rest.slice(i));
    if (m) return balanced(rest, i + m[0].length - 1).inner;
  }
  return null;
}

// Remove `--` line comments and `/* */` block comments without disturbing string
// or dollar-quoted bodies (a policy predicate contains 'app.current_org_id', a
// function body contains arbitrary text).
function stripComments(sql) {
  let out = "";
  for (let i = 0; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") {
      const end = endOfString(sql, i);
      out += sql.slice(i, end < 0 ? sql.length : end + 1);
      i = end < 0 ? sql.length : end;
      continue;
    }
    if (ch === "$") {
      const tag = /^\$\w*\$/.exec(sql.slice(i));
      if (tag) {
        const end = sql.indexOf(tag[0], i + tag[0].length);
        const stop = end < 0 ? sql.length : end + tag[0].length;
        out += sql.slice(i, stop);
        i = stop - 1;
        continue;
      }
    }
    if (ch === "-" && sql[i + 1] === "-") {
      const nl = sql.indexOf("\n", i);
      i = nl < 0 ? sql.length : nl - 1;
      continue;
    }
    if (ch === "/" && sql[i + 1] === "*") {
      const close = sql.indexOf("*/", i + 2);
      i = close < 0 ? sql.length : close + 1;
      continue;
    }
    out += ch;
  }
  return out;
}

// Split into statements on top-level semicolons, keeping semicolons inside string
// and dollar-quoted bodies (plpgsql function bodies are full of them).
function splitStatements(sql) {
  const statements = [];
  let start = 0;
  for (let i = 0; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") {
      i = endOfString(sql, i);
      if (i < 0) break;
      continue;
    }
    if (ch === "$") {
      const tag = /^\$\w*\$/.exec(sql.slice(i));
      if (tag) {
        const end = sql.indexOf(tag[0], i + tag[0].length);
        if (end < 0) break;
        i = end + tag[0].length - 1;
        continue;
      }
    }
    if (ch === ";") {
      const stmt = sql.slice(start, i).trim();
      if (stmt) statements.push(stmt);
      start = i + 1;
    }
  }
  const tail = sql.slice(start).trim();
  if (tail) statements.push(tail);
  return statements;
}

// Split on top-level `sep`, ignoring separators nested in parens or quotes.
function splitTopLevel(text, sep) {
  const parts = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === "'") {
      i = endOfString(text, i);
      if (i < 0) break;
      continue;
    }
    if (ch === "(") depth++;
    else if (ch === ")") depth--;
    else if (ch === sep && depth === 0) {
      parts.push(text.slice(start, i));
      start = i + 1;
    }
  }
  parts.push(text.slice(start));
  return parts;
}

// The text of a single-quoted literal starting at `open`, or null if `at` is not a
// quote. Unescapes doubled quotes.
function literalAt(sql, at) {
  if (sql[at] !== "'") return null;
  const end = endOfString(sql, at);
  if (end < 0) return null;
  return { text: sql.slice(at + 1, end).replace(/''/g, "'"), end };
}

// Consumers set up RLS on many tables through a `DO $$ ... $$` block: either a
// `FOREACH t IN ARRAY ARRAY[...] LOOP EXECUTE format('... %I ...', t) END LOOP`, or
// plain `EXECUTE 'ALTER TABLE ... ROW LEVEL SECURITY'` statements. Expand every
// EXECUTE we can resolve into the concrete SQL it runs so the classifier sees it as
// if written out. An argument that is neither the loop variable nor a literal (a
// function call, a role name, a concatenation) is unresolvable and drops that call —
// otherwise a partial statement could set false state.
function expandDoBlock(stmt) {
  const loop = /FOREACH\s+(\w+)\s+IN\s+ARRAY\s+ARRAY\s*\[([\s\S]*?)\]/i.exec(stmt);
  const loopVar = loop ? loop[1] : null;
  const items = loop
    ? [...loop[2].matchAll(/'((?:[^']|'')*)'/g)].map((m) => m[1].replace(/''/g, "'"))
    : [];

  const expanded = [];
  const execRe = /\bEXECUTE\s+/gi;
  let m;
  while ((m = execRe.exec(stmt))) {
    let i = m.index + m[0].length;

    const literal = literalAt(stmt, i);
    if (literal) {
      // Bare dynamic SQL. A trailing `|| var` concatenation we cannot resolve would
      // make this a fragment, so skip those rather than emit half a statement.
      if (!/^\s*\|\|/.test(stmt.slice(literal.end + 1))) expanded.push(literal.text);
      continue;
    }

    const fmt = /^format\s*\(/i.exec(stmt.slice(i));
    if (!fmt) continue;
    i += fmt[0].length;
    while (/\s/.test(stmt[i] ?? "")) i++;

    let template;
    const tag = /^\$\w*\$/.exec(stmt.slice(i));
    if (tag) {
      const end = stmt.indexOf(tag[0], i + tag[0].length);
      if (end < 0) continue;
      template = stmt.slice(i + tag[0].length, end);
      i = end + tag[0].length;
    } else {
      const tmpl = literalAt(stmt, i);
      if (!tmpl) continue;
      template = tmpl.text;
      i = tmpl.end + 1;
    }

    const close = stmt.indexOf(")", i);
    const args = stmt
      .slice(i, close < 0 ? stmt.length : close)
      .replace(/^\s*,/, "")
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean);
    // Resolve each %I/%s to its argument: the loop variable → the current item, a
    // quoted literal → its text. Any other argument is unresolvable → drop the call.
    const literalArg = (a) => {
      const q = /^'((?:[^']|'')*)'$/.exec(a);
      return q ? q[1].replace(/''/g, "'") : null;
    };
    const resolvable = args.every((a) => a === loopVar || literalArg(a) !== null);
    if (!resolvable) continue;
    const usesLoop = args.includes(loopVar);
    const substitute = (item) => {
      let k = 0;
      return template.replace(/%[Is]/g, () => {
        const a = args[k++];
        return a === loopVar ? item : literalArg(a);
      });
    };
    if (usesLoop) items.forEach((item) => expanded.push(substitute(item)));
    else expanded.push(substitute(null));
  }
  return expanded;
}

// The statements a migration runs, comments removed and every DO block we can
// resolve expanded into the SQL it executes.
function statementsOf(sql) {
  return splitStatements(stripComments(sql)).flatMap((stmt) =>
    /^\s*DO\b/i.test(stmt) ? expandDoBlock(stmt) : [stmt],
  );
}

// Replay migration SQL into a model, then assert the tenant-isolation invariants.
export function analyzeSql(sql) {
  const tables = new Map(); // name -> { tenantColumn }
  const rls = new Map(); // name -> { enabled, forced }
  const policies = new Map(); // name -> [{ policy, verb, using, check }]
  const triggers = []; // { table, verbs, fn }
  const rejecting = new Map(); // fn -> bool (mutation always raises)
  const functions = new Map(); // fn -> body of its latest definition

  const statements = statementsOf(sql);

  // Tenant isolation is owed where request traffic can reach: a table the request
  // role holds no privilege on is a platform or worker relation, whose boundary is
  // the grant itself (the live-database authority check holds that), not a policy.
  const reachable = new Set();
  let everyTableReachable = false;
  for (const stmt of statements) {
    let m;
    if ((m = /^\s*GRANT\s+[\s\S]*?\bON\s+(?:TABLE\s+)?(ALL\s+TABLES\s+IN\s+SCHEMA\s+\w+|[^;]+?)\s+TO\s+([^;]+)/i.exec(stmt)) && /\bapp_tenant\b/i.test(m[2])) {
      if (/^ALL\s+TABLES/i.test(m[1])) everyTableReachable = true;
      else for (const name of m[1].split(",")) reachable.add(stripName(name.trim()));
    }
    if ((m = /^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("?[\w.]+"?)\s+PARTITION\s+OF\s+("?[\w.]+"?)/i.exec(stmt))) {
      // A partition has the parent's columns, and request traffic reaches it
      // wherever it reaches the parent.
      const parent = stripName(m[2]);
      tables.set(stripName(m[1]), { tenantColumn: tables.get(parent)?.tenantColumn ?? null, partitionOf: parent });
    } else if ((m = /^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("?[\w.]+"?)\s*\(/i.exec(stmt))) {
      const name = stripName(m[1]);
      const body = balanced(stmt, stmt.indexOf("(", m.index)).inner;
      let tenantColumn = null;
      for (const part of splitTopLevel(body, ",")) {
        const col = /^\s*"?([a-z_]\w*)"?/i.exec(part);
        if (col && TENANT_COLUMNS.has(col[1].toLowerCase())) {
          tenantColumn = col[1].toLowerCase();
          break;
        }
      }
      tables.set(name, { tenantColumn });
    } else if ((m = /^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?("?[\w.]+"?)/i.exec(stmt))) {
      tables.delete(stripName(m[1]));
    } else if ((m = /^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?("?[\w.]+"?)\s+(ENABLE|FORCE)\s+ROW\s+LEVEL\s+SECURITY/i.exec(stmt))) {
      const name = stripName(m[1]);
      const state = rls.get(name) ?? { enabled: false, forced: false };
      if (/ENABLE/i.test(m[2])) state.enabled = true;
      else state.forced = true;
      rls.set(name, state);
    } else if ((m = /^\s*CREATE\s+POLICY\s+("?\w+"?)\s+ON\s+("?[\w.]+"?)([\s\S]*)$/i.exec(stmt))) {
      const table = stripName(m[2]);
      const rest = m[3];
      const using = clauseExpr(rest, /^USING\s*\(/i);
      const check = clauseExpr(rest, /^WITH\s+CHECK\s*\(/i);
      const head = rest.slice(0, rest.search(/\b(USING|WITH\s+CHECK)\b/i) + 1 || rest.length);
      const verb = /\bFOR\s+(ALL|SELECT|INSERT|UPDATE|DELETE)\b/i.exec(head);
      const list = policies.get(table) ?? [];
      list.push({
        policy: stripName(m[1]),
        roles: /\bTO\s+([\s\S]*?)(?=\bUSING\b|\bWITH\s+CHECK\b|$)/i.exec(rest)?.[1].trim().split(/\s*,\s*/).map(stripName) ?? ["public"],
        verb: verb ? verb[1].toUpperCase() : "ALL",
        // PERMISSIVE (the default) policies OR-combine and grant access; RESTRICTIVE
        // policies only AND-tighten. Only permissive policies admit rows to a verb or
        // can loosen isolation, so the invariants below apply only to them.
        permissive: !/\bAS\s+RESTRICTIVE\b/i.test(head),
        using,
        check,
      });
      policies.set(table, list);
    } else if ((m = /^\s*ALTER\s+POLICY\s+("?\w+"?)\s+ON\s+("?[\w.]+"?)([\s\S]*)$/i.exec(stmt))) {
      const list = policies.get(stripName(m[2])) ?? [];
      const target = list.find((p) => p.policy === stripName(m[1]));
      if (target) {
        const using = clauseExpr(m[3], /^USING\s*\(/i);
        const check = clauseExpr(m[3], /^WITH\s+CHECK\s*\(/i);
        const roles = /\bTO\s+([\s\S]*?)(?=\bUSING\b|\bWITH\s+CHECK\b|$)/i.exec(m[3]);
        if (roles) target.roles = roles[1].trim().split(/\s*,\s*/).map(stripName);
        if (using !== null) target.using = using;
        if (check !== null) target.check = check;
      }
    } else if ((m = /^\s*DROP\s+POLICY\s+(?:IF\s+EXISTS\s+)?("?\w+"?)\s+ON\s+("?[\w.]+"?)/i.exec(stmt))) {
      const list = policies.get(stripName(m[2]));
      if (list) policies.set(stripName(m[2]), list.filter((p) => p.policy !== stripName(m[1])));
    } else if ((m = /^\s*CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+("?[\w.]+"?)\s*\(/i.exec(stmt))) {
      const body = /\bAS\s*(\$\w*\$)([\s\S]*?)\1/i.exec(stmt);
      if (body) {
        rejecting.set(
          stripName(m[1]),
          /RAISE\s+EXCEPTION/i.test(body[2]) && !/RETURN\s+(NEW|OLD)\b/i.test(body[2]),
        );
        functions.set(stripName(m[1]), body[2]);
      }
    } else if ((m = /^\s*DROP\s+FUNCTION\s+(?:IF\s+EXISTS\s+)?("?[\w.]+"?)/i.exec(stmt))) {
      functions.delete(stripName(m[1]));
    } else if ((m = TRIGGER_RE.exec(stmt))) {
      const events = m[1].toUpperCase();
      triggers.push({
        table: stripName(m[2]),
        verbs: MUTATION_VERBS.filter((v) => new RegExp(`\\b${v}\\b`).test(events)),
        fn: stripName(m[3]),
      });
    }
  }

  const guarded = new Map(); // table -> Set(verb) guarded by a mutation-rejecting trigger
  for (const t of triggers) {
    if (!rejecting.get(t.fn)) continue;
    const set = guarded.get(t.table) ?? new Set();
    t.verbs.forEach((v) => set.add(v));
    guarded.set(t.table, set);
  }

  const errors = [];
  for (const [name, { tenantColumn, partitionOf }] of tables) {
    if (!tenantColumn) continue;
    if (!everyTableReachable && !reachable.has(name) && !reachable.has(partitionOf)) continue;
    const state = rls.get(name) ?? { enabled: false, forced: false };
    if (!state.enabled || !state.forced) {
      const missing = [!state.enabled && "ENABLE", !state.forced && "FORCE"].filter(Boolean);
      errors.push(
        `${name}: tenant-scoped (${tenantColumn}) but missing ${missing.join(" + ")} ROW LEVEL SECURITY — rows are not isolated`,
      );
    }

    const list = policies.get(name) ?? [];
    const admitted = new Set(
      list
        .filter((p) => p.permissive && p.using?.trim().toLowerCase() !== "false")
        .flatMap((p) => (p.verb === "ALL" ? ["SELECT", "INSERT", "UPDATE", "DELETE"] : [p.verb])),
    );
    for (const verb of guarded.get(name) ?? []) {
      if (!admitted.has(verb)) {
        errors.push(
          `${name}: an append-only trigger rejects ${verb} but no RLS policy admits ${verb} — under forced RLS the ${verb} matches zero rows and the trigger never fires`,
        );
      }
    }

    for (const p of list) {
      if (!p.permissive) continue;
      for (const [clause, expr] of [["USING", p.using], ["WITH CHECK", p.check]]) {
        if (expr !== null && expr.trim().toLowerCase() !== "false" && !SCOPING_SETTING.test(expr) && !(
          p.roles?.length === 1 &&
          ["app_control_plane", "app_billing_worker", "app_webhook_worker", "app_job_worker"].includes(p.roles[0]) &&
          exactRolePredicate(expr) === `current_user = '${p.roles[0]}'`
        )) {
          errors.push(
            `${name}: policy ${p.policy} has a ${clause} predicate that never references app.current_org_id/app.current_user_id — it may be accidentally unconditional`,
          );
        }
      }
    }
  }

  // A function that creates partitions of a tenant table at runtime must give
  // each one row-level security of its own: ENABLE and FORCE it and create its
  // policies, in its own body or in a function it calls. The model cannot see
  // the partitions it creates, so this is where they are held to the rule.
  const secures = (fn, seen = new Set()) => {
    if (seen.has(fn)) return false;
    seen.add(fn);
    const body = functions.get(fn);
    if (body === undefined) return false;
    if (
      /\bENABLE\s+ROW\s+LEVEL\s+SECURITY\b/i.test(body) &&
      /\bFORCE\s+ROW\s+LEVEL\s+SECURITY\b/i.test(body) &&
      /\bCREATE\s+POLICY\b/i.test(body)
    ) {
      return true;
    }
    for (const call of body.matchAll(/\b(?:public\.)?"?(\w+)"?\s*\(/gi)) {
      const callee = call[1].toLowerCase();
      if (functions.has(callee) && secures(callee, seen)) return true;
    }
    return false;
  };
  for (const [fn, body] of functions) {
    const parents = new Set([...body.matchAll(/\bPARTITION\s+OF\s+("?[\w.]+"?)/gi)].map((p) => stripName(p[1])));
    for (const parent of parents) {
      const tenantColumn = tables.get(parent)?.tenantColumn;
      if (!tenantColumn) continue;
      if (!everyTableReachable && !reachable.has(parent)) continue;
      if (!secures(fn)) {
        errors.push(
          `${parent}: ${fn}() creates partitions without ENABLE + FORCE ROW LEVEL SECURITY and policies of their own — ` +
            `a partition queried by name is not covered by ${parent}'s policies`,
        );
      }
    }
  }
  return errors.sort();
}

// The one predicate shape the background-role exception admits, read the way it
// is written by hand (`current_user = 'app_job_worker'`) and the way pg_dump
// writes it back (`(CURRENT_USER = 'app_job_worker'::name)`): the same predicate,
// so the same verdict.
function exactRolePredicate(expr) {
  let text = expr.trim();
  while (text.startsWith("(") && text.endsWith(")") && balanced(text, 0).end === text.length) {
    text = text.slice(1, -1).trim();
  }
  return text.replace(/\bCURRENT_USER\b/g, "current_user").replace(/'::name\b/g, "'").replace(/\s+/g, " ");
}

// ---------------------------------------------------------------------------
// SECURITY DEFINER functions list pg_temp last in their search_path.
//
// A SECURITY DEFINER function runs with its owner's authority, which here is a
// role that bypasses row-level security or owns the tables. PostgreSQL searches
// the calling session's temporary schema for relation and type names FIRST
// unless the search_path names pg_temp, so a function-level search_path of
// `pg_catalog, public` still lets a caller that may create a temporary table
// shadow a relation the function names unqualified — and the function reads the
// caller's table with the owner's authority. Listing pg_temp last is the fix the
// PostgreSQL documentation gives for exactly this. The rule is held to each
// function's EFFECTIVE setting once every up-migration has run, so a later
// CREATE OR REPLACE, ALTER FUNCTION … SET / RESET search_path or
// ALTER FUNCTION … SECURITY DEFINER counts.

// Multi-word type names, which would otherwise read as `<argname> <type>`.
const MULTIWORD_TYPE =
  /^(?:double\s+precision|(?:character|char)\s+varying|national\s+(?:character|char)(?:\s+varying)?|bit\s+varying|(?:timestamp|time)\s+with(?:out)?\s+time\s+zone|interval(?:\s+(?:year|month|day|hour|minute|second|to))*)(?:\s*\[\s*\d*\s*\])*$/i;

const TYPE_ALIASES = new Map([
  ["int", "integer"], ["int4", "integer"], ["int8", "bigint"], ["int2", "smallint"],
  ["bool", "boolean"], ["float8", "double precision"], ["float4", "real"], ["decimal", "numeric"],
  ["varchar", "character varying"], ["char varying", "character varying"], ["char", "character"],
  ["timestamptz", "timestamp with time zone"], ["timestamp", "timestamp without time zone"],
  ["timetz", "time with time zone"], ["time", "time without time zone"],
]);

// One argument's type, spelled the way PostgreSQL identifies it: no argument
// name, mode, default or type modifier, and the canonical name of an alias.
function argumentType(raw, kind) {
  let text = raw.trim();
  const defaulted = /\s(?:DEFAULT\b|=)/i.exec(text);
  if (defaulted) text = text.slice(0, defaulted.index).trim();
  const mode = /^(IN|OUT|INOUT|VARIADIC)\s+/i.exec(text);
  // A function's OUT arguments are not part of its identity.
  if (mode?.[1].toUpperCase() === "OUT" && kind === "FUNCTION") return null;
  if (mode) text = text.slice(mode[0].length);
  // Type modifiers are never part of an identity, and never part of a name.
  text = text.replace(/\s*\(\s*\d+(?:\s*,\s*\d+)?\s*\)/g, "");
  if (!MULTIWORD_TYPE.test(text)) {
    const named = /^("(?:[^"]|"")+"|[a-z_]\w*)\s+(\S[\s\S]*)$/i.exec(text);
    if (named) text = named[2];
  }
  let type = text
    .toLowerCase()
    .replace(/"/g, "")
    .replace(/^(?:pg_catalog|public)\./, "")
    .replace(/\s+/g, " ")
    .replace(/\s*\[\s*\d*\s*\]/g, "[]")
    .trim();
  const array = /(\[\])+$/.exec(type)?.[0] ?? "";
  const base = type.slice(0, type.length - array.length);
  type = (TYPE_ALIASES.get(base) ?? base) + array;
  return type;
}

function argumentTypes(args, kind) {
  if (!args.trim()) return [];
  return splitTopLevel(args, ",")
    .map((arg) => argumentType(arg, kind))
    .filter((type) => type !== null);
}

// The schemas a `SET search_path {TO | =} …` value names, in order, starting at
// `text`. Returns { elements, end }, or { reset } for `DEFAULT`.
function searchPathValue(text) {
  const elements = [];
  let i = 0;
  for (;;) {
    while (/\s/.test(text[i] ?? "")) i++;
    if (text[i] === "'") {
      const end = endOfString(text, i);
      if (end < 0) break;
      elements.push(text.slice(i + 1, end).replace(/''/g, "'"));
      i = end + 1;
    } else if (text[i] === '"') {
      const end = text.indexOf('"', i + 1);
      if (end < 0) break;
      elements.push(text.slice(i + 1, end));
      i = end + 1;
    } else {
      const word = /^[\w$]+/.exec(text.slice(i));
      if (!word) break;
      if (!elements.length && /^DEFAULT$/i.test(word[0])) return { reset: true, end: i + word[0].length };
      elements.push(word[0].toLowerCase());
      i += word[0].length;
    }
    const comma = /^\s*,/.exec(text.slice(i));
    if (!comma) break;
    i += comma[0].length;
  }
  return { elements, end: i };
}

// The search_path and SECURITY actions in a routine's attribute text, in the
// order they appear: CREATE FUNCTION's clauses and ALTER FUNCTION's actions are
// both read this way.
function routineActions(text) {
  const actions = [];
  const re = /\b(?:(?:EXTERNAL\s+)?SECURITY\s+(DEFINER|INVOKER)\b|SET\s+search_path\s+FROM\s+CURRENT\b|SET\s+search_path\s*(?:TO\b|=)|RESET\s+(?:search_path|ALL)\b)/gi;
  let m;
  while ((m = re.exec(text))) {
    if (m[1]) actions.push({ definer: m[1].toUpperCase() === "DEFINER" });
    else if (/FROM\s+CURRENT/i.test(m[0])) actions.push({ path: "from current" });
    else if (/^RESET/i.test(m[0])) actions.push({ path: null });
    else {
      const value = searchPathValue(text.slice(m.index + m[0].length));
      actions.push({ path: value.reset ? null : value.elements });
      re.lastIndex = m.index + m[0].length + value.end;
    }
  }
  return actions;
}

// A routine's attribute clauses with its body removed, so a body that mentions
// SECURITY DEFINER or SET search_path in a string or a comment is not read as
// one of its own clauses.
function withoutBody(text) {
  let out = "";
  for (let i = 0; i < text.length; i++) {
    const tag = text[i] === "$" ? /^\$\w*\$/.exec(text.slice(i)) : null;
    if (tag) {
      const end = text.indexOf(tag[0], i + tag[0].length);
      i = end < 0 ? text.length : end + tag[0].length - 1;
      out += " ";
      continue;
    }
    const body = /^AS\s*'/i.exec(text.slice(i));
    if (body && (i === 0 || /\W/.test(text[i - 1]))) {
      let end = endOfString(text, i + body[0].length - 1);
      // `AS 'obj_file', 'link_symbol'` for a C function.
      const link = end < 0 ? null : /^\s*,\s*'/.exec(text.slice(end + 1));
      if (link) end = endOfString(text, end + link[0].length);
      i = end < 0 ? text.length : end;
      out += " ";
      continue;
    }
    // A SQL-standard body (BEGIN ATOMIC … END) is the rest of the statement.
    if (/^BEGIN\s+ATOMIC\b/i.test(text.slice(i)) && (i === 0 || /\W/.test(text[i - 1]))) break;
    out += text[i];
  }
  return out;
}

const describePath = (path) =>
  path === null ? "is not set" :
  path === "from current" ? "is taken FROM CURRENT, which no migration can show" :
  `is ${path.map((s) => (s === "" ? "''" : s)).join(", ")}`;

// Unquoted names are folded to lower case when they are read, and a quoted
// "PG_TEMP" names some other schema, so the comparison is exact.
const pinsTempLast = (path) =>
  Array.isArray(path) && path.at(-1) === "pg_temp" && !path.slice(0, -1).includes("pg_temp");

// Replay migration SQL's routine definitions and changes, then report every
// SECURITY DEFINER function or procedure whose search_path does not end in
// pg_temp.
const ROUTINE_NAME = String.raw`((?:"(?:[^"]|"")+"|\w+)(?:\.(?:"(?:[^"]|"")+"|\w+))?)`;

export function definerSearchPathErrors(sql) {
  const routines = new Map(); // "name(types)" -> { name, types, definer, path }
  const errors = [];
  const keyOf = (name, types) => `${name}(${types.join(",")})`;

  // The routines an ALTER or DROP names. Without an argument list the name must
  // be unique, as PostgreSQL requires. With one, a spelling this gate does not
  // normalise can still name the only routine of that name and arity: the
  // database resolved it when the migration ran.
  const resolve = (name, types) => {
    const candidates = [...routines.entries()].filter(([, r]) => r.name === name);
    if (types === null) return candidates.length === 1 ? [candidates[0][0]] : [];
    const exact = candidates.filter(([key]) => key === keyOf(name, types));
    if (exact.length) return exact.map(([key]) => key);
    const sameArity = candidates.filter(([, r]) => r.types.length === types.length);
    return sameArity.length === 1 ? [sameArity[0][0]] : [];
  };

  for (const stmt of statementsOf(sql)) {
    let m;
    if ((m = new RegExp(String.raw`^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(FUNCTION|PROCEDURE)\s+${ROUTINE_NAME}\s*\(`, "i").exec(stmt))) {
      const kind = m[1].toUpperCase();
      const name = stripName(m[2]);
      const { inner, end } = balanced(stmt, m.index + m[0].length - 1);
      const types = argumentTypes(inner, kind);
      // CREATE OR REPLACE replaces every attribute: a clause it leaves out is
      // reset to the default, not kept from the definition it replaces.
      const routine = { name, types, definer: false, path: null };
      for (const action of routineActions(withoutBody(stmt.slice(end)))) Object.assign(routine, action);
      routines.set(keyOf(name, types), routine);
    } else if ((m = new RegExp(String.raw`^\s*ALTER\s+(FUNCTION|PROCEDURE|ROUTINE)\s+${ROUTINE_NAME}\s*`, "i").exec(stmt))) {
      const kind = m[1].toUpperCase() === "PROCEDURE" ? "PROCEDURE" : "FUNCTION";
      const name = stripName(m[2]);
      let rest = stmt.slice(m.index + m[0].length);
      let types = null;
      if (rest.startsWith("(")) {
        const { inner, end } = balanced(rest, 0);
        types = argumentTypes(inner, kind);
        rest = rest.slice(end);
      }
      const targets = resolve(name, types);
      const rename = /^\s*RENAME\s+TO\s+("(?:[^"]|"")+"|\w+)/i.exec(rest);
      if (rename) {
        for (const key of targets) {
          const routine = routines.get(key);
          routines.delete(key);
          routine.name = stripName(rename[1]);
          routines.set(keyOf(routine.name, routine.types), routine);
        }
        continue;
      }
      const actions = routineActions(rest);
      if (!targets.length) {
        // Nothing modelled to weaken, unless the ALTER is what makes it a definer.
        if (actions.some((a) => a.definer)) {
          errors.push(
            `${name}: ALTER ${m[1].toUpperCase()} makes a routine SECURITY DEFINER that the gate cannot resolve ` +
              "to one definition in the migrations, so its search_path cannot be checked",
          );
        }
        continue;
      }
      for (const key of targets) for (const action of actions) Object.assign(routines.get(key), action);
    } else if ((m = /^\s*DROP\s+(FUNCTION|PROCEDURE|ROUTINE)\s+(?:IF\s+EXISTS\s+)?([\s\S]*?)\s*(?:\b(?:CASCADE|RESTRICT)\b\s*)?$/i.exec(stmt))) {
      const kind = m[1].toUpperCase() === "PROCEDURE" ? "PROCEDURE" : "FUNCTION";
      for (const target of splitTopLevel(m[2], ",")) {
        const named = new RegExp(String.raw`^\s*${ROUTINE_NAME}\s*(\(([\s\S]*)\))?\s*$`).exec(target);
        if (!named) continue;
        const types = named[2] === undefined ? null : argumentTypes(named[3], kind);
        for (const key of resolve(stripName(named[1]), types)) routines.delete(key);
      }
    }
  }

  for (const { name, types, definer, path } of routines.values()) {
    if (!definer || pinsTempLast(path)) continue;
    errors.push(
      `${name}(${types.join(", ")}): SECURITY DEFINER, but its search_path ${describePath(path)} — ` +
        "list pg_temp last, or a caller's temporary relations are searched before the schemas it names",
    );
  }
  return errors.sort();
}

export function rlsGateErrors(moduleRoot = MODULE_ROOT) {
  const migrationRoot = join(moduleRoot, "services", "store", "migrations");
  if (!existsSync(migrationRoot)) return [];

  const files = readdirSync(migrationRoot)
    .map((name) => ({ name, match: /^(\d+)_.*\.up\.sql$/.exec(name) }))
    .filter((f) => f.match)
    .map((f) => ({ name: f.name, version: Number(f.match[1]) }))
    .sort((a, b) => a.version - b.version || a.name.localeCompare(b.name));

  const combined = files
    .map((f) => readFileSync(join(migrationRoot, f.name), "utf8"))
    .join("\n;\n");
  return [...analyzeSql(combined), ...definerSearchPathErrors(combined)];
}

function check() {
  const errors = rlsGateErrors();
  if (errors.length) {
    console.error("rls-migration-gate: the store migrations leave a tenant boundary unprotected:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} unprotected tenant boundary check(s). Every tenant-scoped ` +
        "table, and every partition of one, must FORCE row level security, keep " +
        "append-only-guarded verbs reachable by a policy, and scope every policy predicate " +
        "to the tenant setting; every SECURITY DEFINER function must list pg_temp last in " +
        "its search_path.",
    );
    process.exit(1);
  }
  console.log(
    "✓ every tenant-scoped table forces RLS with tenant-scoped, verb-complete policies, " +
      "and every SECURITY DEFINER function lists pg_temp last.",
  );
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "check") check();
  else {
    console.error("usage: rls-migration-gate.mjs check");
    process.exit(2);
  }
}
