#!/usr/bin/env node
// base-integrity — enforce that a saas-starter consumer ADDS files, never MODIFIES base ones.
//
// The base (this module) is composed into each consumer by codefly install/sync — a copy.
// A copy is only as disciplined as the team editing it, so this guard makes the discipline
// mechanical: every base file's sha256 is recorded in `base-manifest.json` (generated FROM
// canonical at sync time and shipped into the consumer). The checker re-hashes those files in
// the consumer and fails on any drift. Files NOT in the manifest are legal side-additions.
//
//   node tools/base-integrity.mjs gen      # (run against CANONICAL) regenerate the manifest
//   node tools/base-integrity.mjs check    # (run in a CONSUMER) fail on any base-file drift
//
// The module root is the parent of tools/ — so this works identically in canonical's `module/`
// and a consumer's `modules/<name>/`, no path config needed. The script hashes itself, so
// tampering with the guard is itself caught.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, relative, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const MODULE_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const MANIFEST_PATH = join(MODULE_ROOT, "tools", "base-manifest.json");
const ALLOW_PATH = join(MODULE_ROOT, "tools", "base-integrity-allow.json");

// Directory names pruned wholesale (build output, deps, VCS) and per-file patterns that are
// generated or inherently per-consumer. Generated files (e.g. the plugin auto-discovery
// registry) are EXCLUDED on purpose — they are produced by base code, not authored, and differ
// legitimately per consumer (each lists its own side-module plugins).
const PRUNE_DIRS = new Set([
  "node_modules", ".next", ".turbo", "dist", "build", "coverage",
  ".git", "vendor", "__pycache__", ".codefly", ".cache", "test-results", "playwright-report",
]);
const isExcludedFile = (rel) =>
  rel === "tools/base-manifest.json" ||      // the manifest can't hash itself
  rel === "tools/base-integrity-allow.json" || // consumer-local escape hatch (logged, not hashed)
  /\.generated\.[a-z]+$/.test(rel) ||         // codegen output (e.g. registry.generated.ts)
  rel.endsWith(".tsbuildinfo") ||             // TypeScript incremental build cache
  rel.endsWith(".DS_Store");

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (PRUNE_DIRS.has(name)) continue;
    const abs = join(dir, name);
    const st = statSync(abs);
    if (st.isDirectory()) walk(abs, out);
    else if (st.isFile()) {
      const rel = relative(MODULE_ROOT, abs);
      if (!isExcludedFile(rel)) out.push(rel);
    }
  }
  return out;
}

const sha = (abs) => createHash("sha256").update(readFileSync(abs)).digest("hex");

// A consumer may compose a SUBSET of the base's services (e.g. mind takes the
// backend — api/store/vault/cache/object-storage — and brings its own gateway, so
// it omits auth-sidecar + the frontend console). Files under an omitted service's
// directory are then legitimately absent and must NOT count as "missing". The
// composed set is the `services:` list in module.codefly.yaml; null = enforce
// everything (canonical itself, or a consumer with no explicit list).
function composedServices() {
  const p = join(MODULE_ROOT, "module.codefly.yaml");
  if (!existsSync(p)) return null;
  const lines = readFileSync(p, "utf8").split("\n");
  let inServices = false;
  const svcs = new Set();
  for (const line of lines) {
    if (/^services:\s*$/.test(line)) { inServices = true; continue; }
    if (!inServices) continue;
    if (/^\S/.test(line)) break;                       // dedent to col 0 → block ended
    const m = line.match(/^\s+-\s+name:\s*(\S+)/);
    if (m) svcs.add(m[1]);
  }
  return svcs.size ? svcs : null;
}

// The service a base file belongs to, or null for module-level files (always enforced).
const serviceOf = (rel) => rel.startsWith("services/") ? rel.split("/")[1] : null;

function gen() {
  const files = walk(MODULE_ROOT).sort();
  const hashes = {};
  for (const rel of files) hashes[rel] = sha(join(MODULE_ROOT, rel));
  const manifest = {
    note: "Base-file integrity manifest for the saas-starter module. Generated FROM canonical by "
      + "`node tools/base-integrity.mjs gen`. Consumers MUST NOT hand-edit base files — only add "
      + "files on the side. Regenerated on every codefly sync from canonical.",
    fileCount: files.length,
    files: hashes,
  };
  writeFileSync(MANIFEST_PATH, JSON.stringify(manifest, null, 2) + "\n");
  console.log(`base-integrity: wrote ${files.length} base-file hashes to tools/base-manifest.json`);
}

function check() {
  if (!existsSync(MANIFEST_PATH)) {
    console.error("base-integrity: no base-manifest.json — run `gen` against canonical first.");
    process.exit(2);
  }
  const { files } = JSON.parse(readFileSync(MANIFEST_PATH, "utf8"));
  const allow = existsSync(ALLOW_PATH) ? JSON.parse(readFileSync(ALLOW_PATH, "utf8")) : {};
  const composed = composedServices();

  const modified = [], missing = [];
  let omitted = 0;
  const omittedSvcs = new Set();
  for (const [rel, want] of Object.entries(files)) {
    const svc = serviceOf(rel);
    if (composed && svc && !composed.has(svc)) { omitted++; omittedSvcs.add(svc); continue; }
    const abs = join(MODULE_ROOT, rel);
    if (!existsSync(abs)) { missing.push(rel); continue; }
    if (sha(abs) !== want) modified.push(rel);
  }
  if (omitted) console.log(`  composed subset: skipped ${omitted} base files for ${omittedSvcs.size} non-composed service(s): ${[...omittedSvcs].sort().join(", ")}`);

  // Anything on disk that isn't a known base file is a legal side-addition.
  const manifestSet = new Set(Object.keys(files));
  const additions = walk(MODULE_ROOT).filter((r) => !manifestSet.has(r));

  // The allowlist is an escape hatch for genuinely per-consumer base files — kept loud so it can
  // never hide drift silently. Entries here are tech debt: prefer a config seam or a side-module.
  const allowed = (list) => list.filter((r) => {
    if (allow[r]) { console.warn(`  ALLOWED (divergence whitelisted: ${allow[r]}): ${r}`); return false; }
    return true;
  });
  const badModified = allowed(modified);
  const badMissing = allowed(missing);

  console.log(`base-integrity: ${Object.keys(files).length} base files, ${additions.length} side-additions.`);
  if (badMissing.length) { console.error(`\n✗ MISSING base files (do not delete base files):`); badMissing.forEach((r) => console.error(`    ${r}`)); }
  if (badModified.length) { console.error(`\n✗ MODIFIED base files (add on the side, never edit the base):`); badModified.forEach((r) => console.error(`    ${r}`)); }

  if (badModified.length || badMissing.length) {
    console.error(`\nFAIL: ${badModified.length} modified, ${badMissing.length} missing. `
      + `Move your change upstream into canonical (making the original stronger), or express it as a side-addition.`);
    process.exit(1);
  }
  console.log("✓ base intact — every base file matches canonical; all consumer changes are additions.");
}

const cmd = process.argv[2];
if (cmd === "gen") gen();
else if (cmd === "check") check();
else { console.error("usage: base-integrity.mjs <gen|check>"); process.exit(2); }
