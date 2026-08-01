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
// (tables, forced-RLS state, policies, triggers) and, for every table carrying a
// tenant column, asserts three invariants. See rlsGateErrors below. Failing
// closed is appropriate — a silently unenforced isolation boundary is worse than
// a build error.
//
//   node tools/rls-migration-gate.mjs check   # fail on any unprotected tenant table
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

const stripName = (raw) =>
  raw.trim().replace(/^public\./i, "").replace(/"/g, "").toLowerCase();

// Walk `sql` from `open` (the index of an opening paren) to its matching close,
// ignoring parens inside single-quoted or dollar-quoted regions. Returns the
// inner text and the index just past the closing paren.
function balanced(sql, open) {
  let depth = 0;
  for (let i = open; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") {
      i = sql.indexOf("'", i + 1);
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

// The parenthesised expression following a clause keyword (USING / WITH CHECK),
// or null when the clause is absent.
function clauseExpr(rest, keyword) {
  const at = rest.search(keyword);
  if (at < 0) return null;
  const open = rest.indexOf("(", at);
  if (open < 0) return null;
  return balanced(rest, open).inner;
}

// Remove `--` line comments and `/* */` block comments without disturbing string
// or dollar-quoted bodies (a policy predicate contains 'app.current_org_id', a
// function body contains arbitrary text).
function stripComments(sql) {
  let out = "";
  for (let i = 0; i < sql.length; i++) {
    const ch = sql[i];
    if (ch === "'") {
      const end = sql.indexOf("'", i + 1);
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
      i = sql.indexOf("'", i + 1);
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
      i = text.indexOf("'", i + 1);
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

// Consumers inherit the starter's idiom of applying one RLS recipe to a list of
// tables through `DO $$ ... FOREACH t IN ARRAY ARRAY[...] LOOP EXECUTE format(...)
// END LOOP $$`. Expand that specific shape into the concrete statements it runs so
// the classifier below sees them as if written out. Any format() argument we can't
// resolve to the loop variable (e.g. a GRANT to current_user) drops that call —
// those never touch table/policy structure.
function expandDoBlock(stmt) {
  const loop = /FOREACH\s+(\w+)\s+IN\s+ARRAY\s+ARRAY\s*\[([\s\S]*?)\]/i.exec(stmt);
  if (!loop) return [];
  const loopVar = loop[1];
  const items = [...loop[2].matchAll(/'((?:[^']|'')*)'/g)].map((m) =>
    m[1].replace(/''/g, "'"),
  );
  if (!items.length) return [];

  const expanded = [];
  const formatRe = /\bformat\s*\(/gi;
  let m;
  while ((m = formatRe.exec(stmt))) {
    let i = m.index + m[0].length;
    while (stmt[i] === " " || stmt[i] === "\n") i++;
    let template;
    const tag = /^\$\w*\$/.exec(stmt.slice(i));
    if (tag) {
      const end = stmt.indexOf(tag[0], i + tag[0].length);
      if (end < 0) continue;
      template = stmt.slice(i + tag[0].length, end);
      i = end + tag[0].length;
    } else if (stmt[i] === "'") {
      const end = stmt.indexOf("'", i + 1);
      if (end < 0) continue;
      template = stmt.slice(i + 1, end).replace(/''/g, "'");
      i = end + 1;
    } else {
      continue;
    }
    const close = stmt.indexOf(")", i);
    const args = stmt
      .slice(i, close < 0 ? stmt.length : close)
      .replace(/^\s*,/, "")
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean);
    if (args.length && !args.every((a) => a === loopVar)) continue;
    for (const item of items) {
      expanded.push(template.replace(/%[Is]/g, () => item));
    }
  }
  return expanded;
}

// Replay migration SQL into a model, then assert the tenant-isolation invariants.
export function analyzeSql(sql) {
  const tables = new Map(); // name -> { tenantColumn }
  const rls = new Map(); // name -> { enabled, forced }
  const policies = new Map(); // name -> [{ policy, verb, using, check }]
  const triggers = []; // { table, verbs, fn }
  const rejecting = new Map(); // fn -> bool (mutation always raises)

  const clean = stripComments(sql);
  const statements = splitStatements(clean).flatMap((stmt) =>
    /^\s*DO\b/i.test(stmt) ? expandDoBlock(stmt) : [stmt],
  );

  for (const stmt of statements) {
    let m;
    if ((m = /^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("?[\w.]+"?)\s*\(/i.exec(stmt))) {
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
    } else if ((m = /^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?("?[\w.]+"?)\s+(ENABLE|FORCE)\s+ROW\s+LEVEL\s+SECURITY/i.exec(stmt))) {
      const name = stripName(m[1]);
      const state = rls.get(name) ?? { enabled: false, forced: false };
      if (/ENABLE/i.test(m[2])) state.enabled = true;
      else state.forced = true;
      rls.set(name, state);
    } else if ((m = /^\s*CREATE\s+POLICY\s+("?\w+"?)\s+ON\s+("?[\w.]+"?)([\s\S]*)$/i.exec(stmt))) {
      const table = stripName(m[2]);
      const rest = m[3];
      const using = clauseExpr(rest, /\bUSING\s*\(/i);
      const check = clauseExpr(rest, /\bWITH\s+CHECK\s*\(/i);
      const head = rest.slice(0, rest.search(/\b(USING|WITH\s+CHECK)\b/i) + 1 || rest.length);
      const verb = /\bFOR\s+(ALL|SELECT|INSERT|UPDATE|DELETE)\b/i.exec(head);
      const list = policies.get(table) ?? [];
      list.push({
        policy: stripName(m[1]),
        verb: verb ? verb[1].toUpperCase() : "ALL",
        using,
        check,
      });
      policies.set(table, list);
    } else if ((m = /^\s*ALTER\s+POLICY\s+("?\w+"?)\s+ON\s+("?[\w.]+"?)([\s\S]*)$/i.exec(stmt))) {
      const list = policies.get(stripName(m[2])) ?? [];
      const target = list.find((p) => p.policy === stripName(m[1]));
      if (target) {
        const using = clauseExpr(m[3], /\bUSING\s*\(/i);
        const check = clauseExpr(m[3], /\bWITH\s+CHECK\s*\(/i);
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
      }
    } else if ((m = /^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+"?\w+"?\s+(?:BEFORE|AFTER|INSTEAD\s+OF)\s+([\s\S]*?)\s+ON\s+("?[\w.]+"?)[\s\S]*?EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+("?[\w.]+"?)/i.exec(stmt))) {
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
  for (const [name, { tenantColumn }] of tables) {
    if (!tenantColumn) continue;
    const state = rls.get(name) ?? { enabled: false, forced: false };
    if (!state.enabled || !state.forced) {
      const missing = [!state.enabled && "ENABLE", !state.forced && "FORCE"].filter(Boolean);
      errors.push(
        `${name}: tenant-scoped (${tenantColumn}) but missing ${missing.join(" + ")} ROW LEVEL SECURITY — rows are not isolated`,
      );
    }

    const list = policies.get(name) ?? [];
    const admitted = new Set(
      list.flatMap((p) => (p.verb === "ALL" ? ["SELECT", "INSERT", "UPDATE", "DELETE"] : [p.verb])),
    );
    for (const verb of guarded.get(name) ?? []) {
      if (!admitted.has(verb)) {
        errors.push(
          `${name}: an append-only trigger rejects ${verb} but no RLS policy admits ${verb} — under forced RLS the ${verb} matches zero rows and the trigger never fires`,
        );
      }
    }

    for (const p of list) {
      for (const [clause, expr] of [["USING", p.using], ["WITH CHECK", p.check]]) {
        if (expr !== null && !SCOPING_SETTING.test(expr)) {
          errors.push(
            `${name}: policy ${p.policy} has a ${clause} predicate that never references app.current_org_id/app.current_user_id — it may be accidentally unconditional`,
          );
        }
      }
    }
  }
  return errors.sort();
}

export function rlsGateErrors(moduleRoot = MODULE_ROOT) {
  const migrationRoot = join(moduleRoot, "services", "store", "migrations");
  if (!existsSync(migrationRoot)) return [];

  const files = readdirSync(migrationRoot)
    .filter((name) => name.endsWith(".up.sql"))
    .map((name) => ({ name, version: Number.parseInt(name, 10) }))
    .sort((a, b) => a.version - b.version || a.name.localeCompare(b.name));

  const combined = files
    .map((f) => readFileSync(join(migrationRoot, f.name), "utf8"))
    .join("\n;\n");
  return analyzeSql(combined);
}

function check() {
  const errors = rlsGateErrors();
  if (errors.length) {
    console.error("rls-migration-gate: tenant-scoped tables are not fully protected:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} unprotected tenant boundary check(s). Every tenant-scoped ` +
        "table must FORCE row level security, keep append-only-guarded verbs reachable by a " +
        "policy, and scope every policy predicate to the tenant setting.",
    );
    process.exit(1);
  }
  console.log("✓ every tenant-scoped table forces RLS with tenant-scoped, verb-complete policies.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "check") check();
  else {
    console.error("usage: rls-migration-gate.mjs check");
    process.exit(2);
  }
}
