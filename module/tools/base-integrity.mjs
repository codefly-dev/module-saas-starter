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
import { join, relative, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");
const MANIFEST_PATH = join(MODULE_ROOT, "tools", "base-manifest.json");
const ALLOW_PATH = join(MODULE_ROOT, "tools", "base-integrity-allow.json");

// Directory names pruned wholesale (build output, deps, VCS) and per-file patterns that are
// generated or inherently per-consumer. Generated files are excluded because base code produces
// them; the application-owned frontend.config.ts is excluded because consumers explicitly list
// their installed compile-time plugin packages there. The frontend lockfile is reproducibly
// regenerated from the protected root manifest plus additive packages/* workspaces. The frontend
// service manifest is generated from protected topology plus the application plugin allowlist.
const PRUNE_DIRS = new Set([
  "node_modules", ".next", ".turbo", "dist", "build", "coverage",
  ".git", "vendor", "__pycache__", ".codefly", ".cache", "test-results", "playwright-report",
]);
export const isExcludedFile = (rel) =>
  rel === "tools/base-manifest.json" ||      // the manifest can't hash itself
  rel === "tools/base-integrity-allow.json" || // consumer-local escape hatch (logged, not hashed)
  rel === "services/store/code/store-migrator" || // `go build ./...` output; source and migrations remain protected
  rel === "services/frontend/code/frontend.config.ts" || // FP-001: application-owned composition root
  rel === "services/frontend/code/package-lock.json" || // FP-010A: generated workspace install graph
  /^services\/[^/]+\/service\.codefly\.yaml$/.test(rel) || // generated from protected topology + explicit application inputs
  /^services\/[^/]+\/builder\//.test(rel) || // service agents regenerate build recipes and companion files
  /\.generated\.[a-z]+$/.test(rel) ||         // codegen output
  rel.endsWith(".tsbuildinfo") ||             // TypeScript incremental build cache
  rel.endsWith("next-env.d.ts") ||            // Next.js-generated env types (regenerated on dev/build)
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

const FRONTEND_CODE_ROOT = join(MODULE_ROOT, "services", "frontend", "code");
const PACKAGE_LOCK_FIELDS = [
  "name",
  "version",
  "dependencies",
  "devDependencies",
  "optionalDependencies",
  "peerDependencies",
  "peerDependenciesMeta",
];
const PACKAGE_DEPENDENCY_FIELDS = [
  "dependencies",
  "devDependencies",
  "optionalDependencies",
  "peerDependencies",
];

function normalizedJSON(value) {
  if (Array.isArray(value)) return value.map(normalizedJSON);
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value)
        .filter(([, entry]) => entry !== undefined)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, entry]) => [key, normalizedJSON(entry)]),
    );
  }
  return value;
}

function packageLockProjection(value) {
  return Object.fromEntries(
    PACKAGE_LOCK_FIELDS
      .filter((field) => value[field] !== undefined)
      .map((field) => [field, value[field]]),
  );
}

// package-lock.json is application-generated, but it is not unchecked. The
// protected root package.json fixes Starter-owned scripts/dependencies and its
// packages/* wildcard. Consumer package.json files are additive side-files.
// This check proves npm's lock contains the exact root/workspace dependency
// metadata and links every installed workspace; npm ci then verifies the full
// transitive graph in CI and the container build.
export function workspaceInstallGraphErrors(frontendCodeRoot = FRONTEND_CODE_ROOT) {
  const rootManifestPath = join(frontendCodeRoot, "package.json");
  const lockPath = join(frontendCodeRoot, "package-lock.json");
  if (!existsSync(rootManifestPath) && !existsSync(lockPath)) return [];
  if (!existsSync(rootManifestPath)) return ["frontend package.json is missing beside its lockfile"];
  if (!existsSync(lockPath)) return ["frontend package-lock.json is missing beside package.json"];

  const errors = [];
  let rootManifest;
  let lock;
  try {
    rootManifest = JSON.parse(readFileSync(rootManifestPath, "utf8"));
    lock = JSON.parse(readFileSync(lockPath, "utf8"));
  } catch (error) {
    return [`frontend package metadata is not valid JSON: ${error.message}`];
  }
  if (JSON.stringify(rootManifest.workspaces) !== JSON.stringify(["packages/*"])) {
    errors.push("protected frontend package.json must declare only the packages/* workspace seam");
  }
  const pinnedLocalDependencies = new Map([
    // CI checks out the SDK at this exact runner-workspace path and pins its
    // commit. Keep the exception narrow until the typed SDK release is
    // published; all product packages still enter through packages/*.
    ["dependencies.codefly", "file:../../../../../../codefly/sdk-js"],
  ]);
  for (const field of PACKAGE_DEPENDENCY_FIELDS) {
    for (const [name, specifier] of Object.entries(rootManifest[field] ?? {})) {
      const dependency = `${field}.${name}`;
      if (
        typeof specifier === "string" &&
        /^(file|link):/.test(specifier) &&
        pinnedLocalDependencies.get(dependency) !== specifier
      ) {
        errors.push(
          `protected frontend package.json ${dependency} must use a published version, not ${specifier}`,
        );
      }
    }
  }
  if (lock.lockfileVersion !== 3 || !lock.packages || typeof lock.packages !== "object") {
    return [...errors, "frontend package-lock.json must be an npm lockfileVersion 3 install graph"];
  }
  const rootLock = lock.packages[""];
  if (!rootLock || JSON.stringify(normalizedJSON(packageLockProjection(rootLock))) !==
      JSON.stringify(normalizedJSON(packageLockProjection(rootManifest)))) {
    errors.push("frontend package-lock.json root metadata does not match protected package.json");
  }
  if (JSON.stringify(rootLock?.workspaces) !== JSON.stringify(rootManifest.workspaces)) {
    errors.push("frontend package-lock.json does not preserve the protected workspace declaration");
  }

  const packagesRoot = join(frontendCodeRoot, "packages");
  const workspaceKeys = [];
  const packageNames = new Set();
  if (existsSync(packagesRoot)) {
    for (const entry of readdirSync(packagesRoot, { withFileTypes: true }).sort((left, right) => left.name.localeCompare(right.name))) {
      if (!entry.isDirectory()) continue;
      const manifestPath = join(packagesRoot, entry.name, "package.json");
      if (!existsSync(manifestPath)) continue;
      const key = `packages/${entry.name}`;
      workspaceKeys.push(key);
      let manifest;
      try {
        manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      } catch (error) {
        errors.push(`${key}/package.json is not valid JSON: ${error.message}`);
        continue;
      }
      if (typeof manifest.name !== "string" || !manifest.name || packageNames.has(manifest.name)) {
        errors.push(`${key}/package.json has a missing or duplicate package name`);
        continue;
      }
      packageNames.add(manifest.name);
      const locked = lock.packages[key];
      if (!locked || JSON.stringify(normalizedJSON(packageLockProjection(locked))) !==
          JSON.stringify(normalizedJSON(packageLockProjection(manifest)))) {
        errors.push(`frontend package-lock.json workspace metadata is stale for ${key}`);
      }
      const link = lock.packages[`node_modules/${manifest.name}`];
      if (!link || link.link !== true || link.resolved !== key) {
        errors.push(`frontend package-lock.json is missing the workspace link for ${manifest.name}`);
      }
    }
  }
  const lockedWorkspaceKeys = Object.keys(lock.packages)
    .filter((key) => /^packages\/[^/]+$/.test(key))
    .sort();
  if (JSON.stringify(lockedWorkspaceKeys) !== JSON.stringify(workspaceKeys.sort())) {
    errors.push("frontend package-lock.json contains a missing or removed packages/* workspace");
  }
  return errors;
}

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
const serviceOf = (rel) => {
  const segments = rel.split("/");
  return segments[0] === "services" && segments.length > 2 ? segments[1] : null;
};

function gen() {
  const installGraphErrors = workspaceInstallGraphErrors();
  if (installGraphErrors.length) {
    installGraphErrors.forEach((error) => console.error(`base-integrity: ${error}`));
    process.exit(1);
  }
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
  const installGraphErrors = workspaceInstallGraphErrors();

  console.log(`base-integrity: ${Object.keys(files).length} base files, ${additions.length} side-additions.`);
  if (badMissing.length) { console.error(`\n✗ MISSING base files (do not delete base files):`); badMissing.forEach((r) => console.error(`    ${r}`)); }
  if (badModified.length) { console.error(`\n✗ MODIFIED base files (add on the side, never edit the base):`); badModified.forEach((r) => console.error(`    ${r}`)); }
  if (installGraphErrors.length) { console.error(`\n✗ INVALID frontend workspace install graph:`); installGraphErrors.forEach((error) => console.error(`    ${error}`)); }

  if (badModified.length || badMissing.length || installGraphErrors.length) {
    console.error(`\nFAIL: ${badModified.length} modified, ${badMissing.length} missing, ${installGraphErrors.length} invalid install-graph checks. `
      + `Move your change upstream into canonical (making the original stronger), or express it as a side-addition.`);
    process.exit(1);
  }
  console.log("✓ base intact — every base file matches canonical; all consumer changes are additions.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "gen") gen();
  else if (cmd === "check") check();
  else { console.error("usage: base-integrity.mjs <gen|check>"); process.exit(2); }
}
