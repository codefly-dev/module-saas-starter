#!/usr/bin/env node
// migration-pairing-gate — a database-free static gate over every service's
// migration files.
//
// golang-migrate validates the WHOLE set when it loads, before running a single
// statement: every version must have both an `.up.sql` and a `.down.sql`, and no
// version may repeat. A missing half is invisible until deploy — the migration
// Job then dies at startup with "no migration found for version N: read down …
// file does not exist" on every cell, having shipped a green build. (This is not
// hypothetical: v0.0.46 shipped 105_org_generic_settings.up.sql with its .down.sql
// dropped by a stale sync, and crashlooped every store on boot.)
//
// This walks each `services/<svc>/migrations` directory and, per version number,
// asserts exactly one up file and exactly one down file — failing on an orphaned
// half or a duplicated version, the sets golang-migrate refuses to load. It also
// requires the up and down of a version to share a title: golang-migrate keys
// only on the number and would load a mismatch, but a half-applied rename is a
// human error worth catching, so the gate is deliberately one notch stricter
// there. Zero-tolerance.
//
//   node tools/migration-pairing-gate.mjs check   # fail on any orphan or duplicate
//
// The module root is the parent of tools/, so this works identically in
// canonical's `module/` and a consumer's `modules/<name>/`.

import { readdirSync, existsSync, statSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

const MIGRATION_RE = /^(\d+)_(.+)\.(up|down)\.sql$/;

// Every version in a single service's migrations directory must carry exactly one
// up file and one down file, both sharing a title. A version with only one half is
// an orphan golang-migrate refuses to load; a version that appears twice in one
// direction is an ambiguous "duplicate migration version".
function directionPairingErrors(service, files) {
  const versions = new Map(); // numeric version -> { up: [titles], down: [titles] }
  for (const name of files) {
    const match = MIGRATION_RE.exec(name);
    if (!match) continue;
    const [, version, title, direction] = match;
    // golang-migrate parses the version with ParseUint, so `007` and `7` are the
    // same version and collide. Key by the number, not the zero-padded text, or a
    // real duplicate slips through as two well-formed versions.
    const key = Number(version);
    const entry = versions.get(key) ?? { up: [], down: [] };
    entry[direction].push(title);
    versions.set(key, entry);
  }

  const errors = [];
  for (const [version, { up, down }] of versions) {
    if (up.length > 1 || down.length > 1) {
      const names = [...new Set([...up, ...down])].sort();
      errors.push(`${service}: migration version ${version} is duplicated across ${names.join(", ")}`);
    } else if (up.length === 0) {
      errors.push(`${service}: migration version ${version} (${down[0]}) has a .down.sql but no .up.sql`);
    } else if (down.length === 0) {
      errors.push(`${service}: migration version ${version} (${up[0]}) has an .up.sql but no .down.sql`);
    } else if (up[0] !== down[0]) {
      errors.push(`${service}: migration version ${version} up (${up[0]}) and down (${down[0]}) titles differ`);
    }
  }
  return errors;
}

export function migrationPairingErrors(moduleRoot = MODULE_ROOT) {
  const servicesRoot = join(moduleRoot, "services");
  if (!existsSync(servicesRoot)) return [];

  const errors = [];
  for (const service of readdirSync(servicesRoot).sort()) {
    const migrations = join(servicesRoot, service, "migrations");
    if (!existsSync(migrations) || !statSync(migrations).isDirectory()) continue;
    errors.push(...directionPairingErrors(service, readdirSync(migrations).sort()));
  }
  return errors.sort();
}

function check() {
  const errors = migrationPairingErrors();
  if (errors.length) {
    console.error("migration-pairing-gate: migration sets golang-migrate would refuse to load:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} orphaned or duplicated migration version(s). Every version ` +
        "must ship exactly one .up.sql and one matching .down.sql.",
    );
    process.exit(1);
  }
  console.log("✓ every migration version ships a matched up + down with no duplicates.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "check") check();
  else {
    console.error("usage: migration-pairing-gate.mjs check");
    process.exit(2);
  }
}
