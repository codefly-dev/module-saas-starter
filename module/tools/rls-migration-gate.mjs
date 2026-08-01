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
      const using = clauseExpr(rest, /^USING\s*\(/i);
      const check = clauseExpr(rest, /^WITH\s+CHECK\s*\(/i);
      const head = rest.slice(0, rest.search(/\b(USING|WITH\s+CHECK)\b/i) + 1 || rest.length);
      const verb = /\bFOR\s+(ALL|SELECT|INSERT|UPDATE|DELETE)\b/i.exec(head);
      const list = policies.get(table) ?? [];
      list.push({
        policy: stripName(m[1]),
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
      list
        .filter((p) => p.permissive)
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
    .map((name) => ({ name, match: /^(\d+)_.*\.up\.sql$/.exec(name) }))
    .filter((f) => f.match)
    .map((f) => ({ name: f.name, version: Number(f.match[1]) }))
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
