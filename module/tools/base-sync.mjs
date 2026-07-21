#!/usr/bin/env node

// Manifest-driven SaaS Starter recomposition.
//
// The canonical base-integrity manifest is both the integrity contract and the
// exact copy set. Syncing this set (instead of rsyncing the module directory)
// preserves consumer side-additions, generated service manifests, the
// application-owned frontend composition root, and dependency lockfiles.
//
// Usage from the canonical module:
//   node tools/base-sync.mjs --target /path/to/consumer/module
//   node tools/base-sync.mjs --target /path/to/consumer/module --apply
//
// Modified protected files and side-addition collisions fail closed. After
// reviewing the dry-run, an operator may acknowledge promoted base edits with
// --replace-modified and newly-canonical paths with --replace-collisions.

import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
} from "node:fs";
import { createHash } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { requiredAdditionsErrors } from "./base-integrity.mjs";

const SOURCE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const MANIFEST_REL = "tools/base-manifest.json";
const ALLOW_REL = "tools/base-integrity-allow.json";

const sha = (path) => createHash("sha256").update(readFileSync(path)).digest("hex");
const readJSON = (path) => JSON.parse(readFileSync(path, "utf8"));

function parseArgs(argv) {
  const options = {
    apply: false,
    replaceModified: false,
    replaceCollisions: false,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--target") options.target = argv[++i];
    else if (arg === "--apply") options.apply = true;
    else if (arg === "--replace-modified") options.replaceModified = true;
    else if (arg === "--replace-collisions") options.replaceCollisions = true;
    else throw new Error(`unknown argument ${arg}`);
  }
  if (!options.target) throw new Error("--target is required");
  options.target = resolve(options.target);
  if (options.target === SOURCE_ROOT) throw new Error("target must not be the canonical source module");
  return options;
}

function buildPlan(options, sourceRoot = SOURCE_ROOT) {
  const sourceManifestPath = join(sourceRoot, MANIFEST_REL);
  const targetManifestPath = join(options.target, MANIFEST_REL);
  if (!existsSync(sourceManifestPath)) throw new Error(`canonical manifest missing: ${sourceManifestPath}`);
  if (!existsSync(targetManifestPath)) throw new Error(`consumer manifest missing: ${targetManifestPath}`);

  const sourceManifest = readJSON(sourceManifestPath);
  const targetManifest = readJSON(targetManifestPath);
  const allowPath = join(options.target, ALLOW_REL);
  const allow = existsSync(allowPath) ? readJSON(allowPath) : {};
  const sourceFiles = sourceManifest.files ?? {};
  const targetFiles = targetManifest.files ?? {};
  const plan = {
    unchanged: [],
    create: [],
    update: [],
    modified: [],
    collision: [],
    remove: [],
    released: [],
    staleModified: [],
    allowed: [],
    sourceInvalid: [],
    requiredAdditionErrors: requiredAdditionsErrors(options.target, allow),
    sourceManifest,
  };

  for (const [rel, expectedHash] of Object.entries(sourceFiles)) {
    if (allow[rel]) {
      plan.allowed.push(rel);
      continue;
    }
    const source = join(sourceRoot, rel);
    const target = join(options.target, rel);
    if (!existsSync(source) || sha(source) !== expectedHash) {
      plan.sourceInvalid.push(rel);
      continue;
    }
    if (!existsSync(target)) {
      plan.create.push(rel);
      continue;
    }
    const targetHash = sha(target);
    if (targetHash === expectedHash) {
      plan.unchanged.push(rel);
    } else if (!(rel in targetFiles)) {
      plan.collision.push(rel);
    } else if (targetHash === targetFiles[rel]) {
      plan.update.push(rel);
    } else {
      plan.modified.push(rel);
    }
  }

  for (const [rel, oldHash] of Object.entries(targetFiles)) {
    if (rel in sourceFiles || allow[rel]) continue;
    // The canonical source may deliberately stop protecting a generated or
    // application-owned path while leaving its local artifact on disk. Release
    // target ownership in that case: preserve the consumer file as a legal
    // side-addition instead of treating it as an upstream deletion.
    if (existsSync(join(sourceRoot, rel))) {
      plan.released.push(rel);
      continue;
    }
    const target = join(options.target, rel);
    if (!existsSync(target)) continue;
    if (sha(target) === oldHash) plan.remove.push(rel);
    else plan.staleModified.push(rel);
  }

  return plan;
}

function printGroup(label, values) {
  if (!values.length) return;
  console.log(`\n${label} (${values.length})`);
  for (const value of values) console.log(`  ${value}`);
}

function printPlan(plan, options, sourceRoot = SOURCE_ROOT) {
  console.log(`base-sync: ${options.apply ? "apply" : "dry-run"}`);
  console.log(`  source: ${sourceRoot}`);
  console.log(`  target: ${options.target}`);
  console.log(`  unchanged=${plan.unchanged.length} create=${plan.create.length} update=${plan.update.length} remove=${plan.remove.length}`);
  console.log(`  modified=${plan.modified.length} collisions=${plan.collision.length} released=${plan.released.length} stale-modified=${plan.staleModified.length} allowed=${plan.allowed.length}`);
  printGroup("CREATE", plan.create);
  printGroup("UPDATE", plan.update);
  printGroup("REMOVE UPSTREAM-DELETED BASE", plan.remove);
  printGroup("RELEASE AS CONSUMER SIDE-ADDITION", plan.released);
  printGroup("MODIFIED PROTECTED FILES", plan.modified);
  printGroup("SIDE-ADDITION COLLISIONS", plan.collision);
  printGroup("MODIFIED FILES DELETED UPSTREAM", plan.staleModified);
  printGroup("ALLOWLISTED CONSUMER FILES", plan.allowed);
  printGroup("INVALID CANONICAL MANIFEST ENTRIES", plan.sourceInvalid);
  printGroup("INVALID REQUIRED CONSUMER ADDITIONS", plan.requiredAdditionErrors);
}

function assertApplicable(plan, options) {
  if (plan.sourceInvalid.length) {
    throw new Error("canonical manifest is stale; regenerate it only after canonical release gates pass");
  }
  if (plan.requiredAdditionErrors.length) {
    throw new Error("required consumer additions are missing or invalid; restore the product overlay before syncing");
  }
  if (plan.modified.length && !options.replaceModified) {
    throw new Error("modified protected files require --replace-modified after review");
  }
  if (plan.collision.length && !options.replaceCollisions) {
    throw new Error("new canonical paths collide with side-additions; reconcile or pass --replace-collisions after review");
  }
  if (plan.staleModified.length) {
    throw new Error("modified protected files deleted upstream require manual reconciliation");
  }
}

function copyBaseFile(rel, targetRoot, sourceRoot = SOURCE_ROOT) {
  const source = join(sourceRoot, rel);
  const target = join(targetRoot, rel);
  mkdirSync(dirname(target), { recursive: true });
  copyFileSync(source, target);
  chmodSync(target, statSync(source).mode & 0o777);
}

function applyPlan(plan, options, sourceRoot = SOURCE_ROOT) {
  assertApplicable(plan, options);
  for (const rel of [...plan.create, ...plan.update, ...plan.modified, ...plan.collision]) {
    copyBaseFile(rel, options.target, sourceRoot);
  }
  for (const rel of plan.remove) rmSync(join(options.target, rel));
  copyBaseFile(MANIFEST_REL, options.target, sourceRoot);
}

export { applyPlan, assertApplicable, buildPlan, parseArgs };

function main() {
  try {
    const options = parseArgs(process.argv.slice(2));
    const plan = buildPlan(options);
    printPlan(plan, options);
    if (options.apply) {
      applyPlan(plan, options);
      console.log("\nbase-sync: consumer base and manifest updated successfully");
    } else {
      try {
        assertApplicable(plan, options);
        console.log("\nbase-sync: dry-run is applicable; rerun with --apply");
      } catch (error) {
        console.log(`\nbase-sync: dry-run requires review: ${error.message}`);
      }
    }
  } catch (error) {
    console.error(`base-sync: ${error.message}`);
    process.exit(1);
  }
}

if (resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) main();
