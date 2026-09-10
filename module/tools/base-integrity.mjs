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

import { rlsGateErrors } from "./rls-migration-gate.mjs";
import { migrationPairingErrors } from "./migration-pairing-gate.mjs";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");
const MANIFEST_PATH = join(MODULE_ROOT, "tools", "base-manifest.json");
const MANIFEST_NOTE =
  "Base-file integrity manifest for the saas-starter module. Generated FROM canonical by "
  + "`node tools/base-integrity.mjs gen`. Consumers MUST NOT hand-edit base files — only add "
  + "files on the side. Regenerated on every codefly sync from canonical.";
const ALLOW_PATH = join(MODULE_ROOT, "tools", "base-integrity-allow.json");
const CAPABILITY_MANIFEST_REL =
  "services/frontend/code/src/features/trust/capability-manifest.json";
const CAPABILITY_STATES = [
  "absent",
  "implemented",
  "configured",
  "operationally_verified",
  "externally_attested",
];
const CAPABILITY_RESPONSIBILITIES = new Set([
  "starter",
  "provider",
  "adopter",
  "shared",
]);
const EVIDENCE_STATES = new Set([
  "operationally_verified",
  "externally_attested",
]);
const EVIDENCE_STATUSES = new Set(["current", "expired", "revoked", "rejected"]);
const EVIDENCE_VISIBILITIES = new Set(["private", "public_summary"]);

const UNSUPPORTED_PUBLIC_CLAIMS = [
  ["fixed backup retention", /\bbackups? (?:are|is) retained for \d+/i],
  ["unverified point-in-time recovery", /\bpoint-in-time recovery capability\b/i],
  ["executed DPA availability", /\b(?:a )?data processing agreement \(?DPA\)? is available\b/i],
  ["unverified penetration test", /\bannual third-party penetration testing is conducted\b/i],
  ["unverified incident delivery", /\bstatus updates are provided via email\b/i],
  ["unapproved incident target", /\btarget response:\s*\d+/i],
  ["fixed database encryption claim", /\bdatabase-level encryption at rest \(AES-256\)/i],
  ["unverified transport enforcement", /\bTLS 1\.2\+ enforced on all external connections\b/i],
  ["unapproved SLA claim", /\bincident response procedures with documented SLAs\b/i],
  ["completed privacy export", /\bfull export of (?:their|your) personal data\b/i],
  ["completed archive export", /\bZIP archive containing all (?:their|your) account data\b/i],
  ["completed privacy deletion", /\bpermanently delete (?:their|your) account and all associated data\b/i],
  ["immediate physical erasure", /\bremove all (?:their|your) data from (?:our|the) servers\b/i],
  ["compliance status", /\bGDPR compliance\b/i],
  ["unsupported compliance readiness", /\bcompliance ready\b[\s\S]{0,120}\byes\b/i],
  ["unsupported production readiness", /\bproduction-grade from day one\b/i],
  ["unverified customer endorsement", /\btrusted by teams shipping\b/i],
  ["unsupported assurance path", /\bSOC ?2\s*\/\s*enterprise-compliant path\b/i],
  ["unverified audit immutability", /\btamper-evident copy outside the platform\b/i],
  [
    "direct application OTLP endpoint",
    /\bapplications?\s+(?:continue\s+to\s+)?send\s+OTLP\s+(?:through|via)\b[\s\S]{0,80}\bOTEL_EXPORTER_OTLP_ENDPOINT\b/i,
  ],
];

// Directory names pruned wholesale (build output, deps, VCS) and per-file patterns that are
// generated or inherently per-consumer. Generated files are excluded because base code produces
// them; the application-owned frontend.config.ts is excluded because consumers explicitly list
// their installed compile-time plugin packages there. The frontend lockfile is reproducibly
// regenerated from the protected root manifest plus additive packages/* workspaces. The frontend
// service manifest is generated from protected topology plus the application plugin allowlist.
const PRUNE_DIRS = new Set([
  "node_modules", ".next", ".turbo", "dist", "build", "coverage",
  ".git", "vendor", "__pycache__", ".codefly", ".cache", ".nix-cache", "test-results", "playwright-report",
]);
export const isExcludedFile = (rel) =>
  rel === "tools/base-manifest.json" ||      // the manifest can't hash itself
  rel === "tools/base-integrity-allow.json" || // consumer-local escape hatch (logged, not hashed)
  rel === "module.codefly.yaml" ||           // consumer identity and exact service inventory
  rel === "deployment/generated/service-topology.json" || // generated from the consumer topology
  rel.startsWith("deployment/kustomize/") || // generated from workspace/environment GitOps inputs
  rel === "services/store/code/store-migrator" || // `go build ./...` output; source and migrations remain protected
  rel.includes("/.nix-cache/") ||              // per-service Nix evaluation cache; never release source
  /^services\/[^/]+\/nix\//.test(rel) ||       // service-agent runtime materialization; ignored by git
  rel === "services/frontend/code/frontend.config.ts" || // FP-001: application-owned composition root
  rel === "services/frontend/code/package-lock.json" || // FP-010A: generated workspace install graph
  /^(services\/[^/]+\/)?configurations\/.*\.secret\.[^/]+$/.test(rel) || // local secret material is SDK/runtime-owned, never canonical base
  /^services\/[^/]+\/service\.codefly\.yaml$/.test(rel) || // generated from protected topology + explicit application inputs
  /^services\/[^/]+\/builder\//.test(rel) || // service agents regenerate build recipes and companion files
  /\.generated\.[a-z]+$/.test(rel) ||         // codegen output
  rel.endsWith(".tsbuildinfo") ||             // TypeScript incremental build cache
  rel.endsWith("next-env.d.ts") ||            // Next.js-generated env types (regenerated on dev/build)
  rel.endsWith(".DS_Store");

function walk(dir, out = [], base = MODULE_ROOT) {
  for (const name of readdirSync(dir)) {
    if (PRUNE_DIRS.has(name)) continue;
    const abs = join(dir, name);
    const st = statSync(abs);
    if (st.isDirectory()) walk(abs, out, base);
    else if (st.isFile()) {
      const rel = relative(base, abs);
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

// Semver for workspace→workspace ranges, FAIL CLOSED beyond it. `module/tools`
// runs on bare node with no node_modules (the Base-manifest job installs
// nothing), so the real `semver` package is unavailable and this is hand-rolled.
// It must accept everything npm accepts in these edges, because a false reject
// here hard-fails "Base manifest integrity" — a gate on every PR in the repo.

// A version: "1.2.3", "v1.2.3", "1.2.3-rc.1", "1.2.3-rc.1+build".
// Prerelease identifiers are kept so precedence can be compared properly; build
// metadata is stripped, which is what semver says to do when comparing.
export function parseSemver(text) {
  if (typeof text !== "string") return null;
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(text.trim());
  if (!match) return null;
  const pre = match[4] === undefined
    ? null
    : match[4].split(".").map((part) => (/^\d+$/.test(part) ? Number(part) : part));
  return { triple: [Number(match[1]), Number(match[2]), Number(match[3])], pre };
}

// A comparator operand, which npm lets you write partially or with an `x`
// placeholder: "1", "1.2", "1.2.3", "1.x", "1.2.*", "*", "". `specified` records
// how many of major/minor/patch were actually pinned so `^`, `~` and bare
// X-ranges can derive the bounds npm derives.
function parseOperand(text) {
  const trimmed = text.trim();
  if (trimmed === "" || trimmed === "*" || /^[xX]$/.test(trimmed)) {
    return { triple: [0, 0, 0], pre: null, specified: 0 };
  }
  const match = /^v?(\d+|[xX*])(?:\.(\d+|[xX*]))?(?:\.(\d+|[xX*]))?(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(trimmed);
  if (!match) return null;
  const parts = [match[1], match[2], match[3]];
  const triple = [0, 0, 0];
  let specified = 0;
  for (let index = 0; index < 3; index += 1) {
    const part = parts[index];
    if (part === undefined || /^[xX*]$/.test(part)) break;
    triple[index] = Number(part);
    specified += 1;
  }
  if (specified === 0) return { triple: [0, 0, 0], pre: null, specified: 0 };
  const pre = match[4] === undefined
    ? null
    : match[4].split(".").map((part) => (/^\d+$/.test(part) ? Number(part) : part));
  return { triple, pre, specified };
}

const compareTriples = (left, right) =>
  left[0] - right[0] || left[1] - right[1] || left[2] - right[2];

// Semver 2.0 precedence: equal triples, then a version WITH a prerelease sorts
// before one without; otherwise identifiers compare left to right, numeric
// before alphanumeric.
function comparePre(left, right) {
  if (left === null && right === null) return 0;
  if (left === null) return 1;
  if (right === null) return -1;
  for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
    const a = left[index];
    const b = right[index];
    if (a === undefined) return -1;
    if (b === undefined) return 1;
    const aNumeric = typeof a === "number";
    const bNumeric = typeof b === "number";
    if (aNumeric !== bNumeric) return aNumeric ? -1 : 1;
    if (a !== b) return a < b ? -1 : 1;
  }
  return 0;
}

const compareSemver = (left, right) =>
  compareTriples(left.triple, right.triple) || comparePre(left.pre, right.pre);

// The upper bound npm gives `^`, including its leading-zero special cases:
// ^0.2.1 allows <0.3.0 (not <1.0.0) and ^0.0.3 allows <0.0.4, because in a 0.x
// line every minor is potentially breaking.
function caretUpper({ triple, specified }) {
  const [major, minor, patch] = triple;
  if (major !== 0) return [major + 1, 0, 0];
  if (specified === 1) return [1, 0, 0]; // ^0 → <1.0.0
  if (minor !== 0) return [0, minor + 1, 0];
  if (specified === 2) return [0, 1, 0]; // ^0.0 → <0.1.0
  return [0, 0, patch + 1];
}

// Expand one comparator into concrete bounds. Returns a list of {op, semver}
// predicates, or null when the shape is not understood.
function expandComparator(token) {
  const match = /^(>=|<=|>|<|=|\^|~)?\s*(.*)$/.exec(token);
  if (!match) return null;
  const [, operator = "", rest] = match;
  const operand = parseOperand(rest);
  if (operand === null) return null;
  const { triple, pre, specified } = operand;
  const lower = { triple, pre };
  if (specified === 0) return []; // `*` / `x` — no constraint at all.
  if (operator === "^") {
    return [
      { op: ">=", semver: lower },
      { op: "<", semver: { triple: caretUpper(operand), pre: null } },
    ];
  }
  if (operator === "~") {
    const upper = specified === 1 ? [triple[0] + 1, 0, 0] : [triple[0], triple[1] + 1, 0];
    return [
      { op: ">=", semver: lower },
      { op: "<", semver: { triple: upper, pre: null } },
    ];
  }
  if (operator === "" || operator === "=") {
    // A fully pinned version is exact; a partial one is an X-range.
    if (specified === 3) return [{ op: "=", semver: lower }];
    const upper = specified === 1 ? [triple[0] + 1, 0, 0] : [triple[0], triple[1] + 1, 0];
    return [
      { op: ">=", semver: lower },
      { op: "<", semver: { triple: upper, pre: null } },
    ];
  }
  // Ordering comparators zero-fill a partial operand, which is what npm does.
  return [{ op: operator, semver: lower }];
}

// npm excludes a prerelease version from a range unless some comparator in the
// same branch pins the SAME major.minor.patch and itself carries a prerelease.
// So 1.5.0-rc.1 does NOT satisfy ^1.0.0, but 0.3.0-rc.2 does satisfy >=0.3.0-rc.1.
function prereleaseAllowed(target, predicates) {
  if (target.pre === null) return true;
  return predicates.some(
    ({ semver }) => semver.pre !== null && compareTriples(semver.triple, target.triple) === 0,
  );
}

export function satisfiesWorkspaceRange(range, version) {
  const target = parseSemver(version);
  // The caller checks the version separately and reports it as such; guard here
  // so this stays a total function.
  if (target === null || typeof range !== "string") return null;
  const branches = range.split("||").map((branch) => branch.trim());
  let anySatisfied = false;
  for (const branch of branches) {
    // Split on whitespace, then re-join an operator that was written detached
    // from its operand (">= 1.2.3" is legal npm and must not be rejected).
    const rawTokens = branch.split(/\s+/).filter(Boolean);
    const tokens = [];
    for (const raw of rawTokens) {
      if (/^(>=|<=|>|<|=|\^|~)$/.test(raw)) tokens.push({ pendingOperator: raw });
      else if (tokens.length > 0 && tokens[tokens.length - 1].pendingOperator !== undefined) {
        tokens[tokens.length - 1] = { token: tokens[tokens.length - 1].pendingOperator + raw };
      } else tokens.push({ token: raw });
    }
    if (tokens.length === 0) return null; // An empty branch ("a || ") is malformed.
    const predicates = [];
    for (const entry of tokens) {
      if (entry.token === undefined) return null; // Dangling operator, no operand.
      const expanded = expandComparator(entry.token);
      if (expanded === null) return null; // Hyphen ranges and anything else: fail closed.
      predicates.push(...expanded);
    }
    let branchSatisfied = prereleaseAllowed(target, predicates);
    if (branchSatisfied) {
      for (const { op, semver } of predicates) {
        const ordering = compareSemver(target, semver);
        const ok =
          op === ">=" ? ordering >= 0
          : op === ">" ? ordering > 0
          : op === "<=" ? ordering <= 0
          : op === "<" ? ordering < 0
          : ordering === 0;
        if (!ok) {
          branchSatisfied = false;
          break;
        }
      }
    }
    if (branchSatisfied) anySatisfied = true;
  }
  return anySatisfied;
}

// Every dependency edge that points at another workspace in this repo must be
// satisfiable BY that workspace. npm links a workspace only when the declared
// range covers the workspace's own version; when it does not, npm silently
// stops treating it as local and goes to the public registry for it instead —
// where these packages do not exist, so `npm ci` dies with E404 (and would
// install a stranger's package if the name were ever squatted).
//
// The metadata equality checks above cannot see this: they prove the lockfile
// AGREES with each manifest, and a stale exact pin copied faithfully into the
// lockfile agrees perfectly while being unsatisfiable. That is exactly how
// `@codefly-dev/saas-ui` kept requiring `@codefly-dev/saas-sdk@0.2.0` after the
// SDK workspace moved to 0.2.1: this gate reported "in sync" while three CI jobs
// died on `npm ci`. Agreement is not satisfiability.
export function workspaceLinkSatisfactionErrors({ root, workspaces }) {
  const versions = new Map();
  const errors = [];
  for (const { label, manifest } of workspaces) {
    if (typeof manifest?.name !== "string" || typeof manifest.version !== "string") continue;
    // Report an unreadable VERSION against the workspace that declares it. The
    // range is the other half of the comparison and is usually innocent, so
    // blaming it here sends the reader to the wrong file.
    if (parseSemver(manifest.version) === null) {
      errors.push(
        `${label} version "${manifest.version}" is not a semver this gate can read, ` +
          "so its workspace links cannot be checked",
      );
      continue;
    }
    versions.set(manifest.name, manifest.version);
  }
  for (const { label, manifest } of [{ label: "package.json", manifest: root }, ...workspaces]) {
    for (const field of PACKAGE_DEPENDENCY_FIELDS) {
      for (const [name, range] of Object.entries(manifest?.[field] ?? {})) {
        const version = versions.get(name);
        if (version === undefined) continue; // Not a local workspace (or already reported).
        const satisfied = satisfiesWorkspaceRange(range, version);
        if (satisfied === null) {
          errors.push(
            `${label} ${field}.${name} = "${range}" uses a range this gate cannot evaluate; ` +
              "use an exact, ^, ~, x-range or comparator range so the workspace link stays checkable",
          );
        } else if (!satisfied) {
          errors.push(
            `${label} ${field}.${name} = "${range}" is not satisfied by workspace ${name}@${version}, ` +
              "so npm ci resolves it from the public registry instead of the local workspace",
          );
        }
      }
    }
  }
  return errors;
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
  for (const field of PACKAGE_DEPENDENCY_FIELDS) {
    for (const [name, specifier] of Object.entries(rootManifest[field] ?? {})) {
      const dependency = `${field}.${name}`;
      if (
        typeof specifier === "string" &&
        /^(file|link):/.test(specifier)
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
  const workspaceManifests = [];
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
      workspaceManifests.push({ label: `${key}/package.json`, manifest });
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
  errors.push(
    ...workspaceLinkSatisfactionErrors({ root: rootManifest, workspaces: workspaceManifests }),
  );
  return errors;
}

// Consumer additions are intentionally outside the canonical hash manifest,
// but a product may still require selected composition roots to exist after a
// base sync. The application-owned allow file is the durable contract because
// sync never replaces it. Values are human-readable reasons for the requirement.
export function requiredAdditionsErrors(moduleRoot, allow = {}) {
  const required = allow.requiredAdditions;
  if (required === undefined) return [];
  if (!required || Array.isArray(required) || typeof required !== "object") {
    return ["base-integrity-allow.json requiredAdditions must be a path-to-reason object"];
  }

  const errors = [];
  for (const [rel, reason] of Object.entries(required)) {
    const abs = resolve(moduleRoot, rel);
    const local = relative(moduleRoot, abs);
    if (!rel || local.startsWith("..") || resolve(abs) === resolve(moduleRoot)) {
      errors.push(`required addition path escapes the module: ${rel || "<empty>"}`);
      continue;
    }
    if (typeof reason !== "string" || !reason.trim()) {
      errors.push(`required addition ${rel} must declare a non-empty reason`);
    }
    if (!existsSync(abs)) errors.push(`required consumer addition is missing: ${rel}`);
  }
  return errors;
}

export function capabilityManifestErrors(manifest) {
  if (!manifest || Array.isArray(manifest) || typeof manifest !== "object") {
    return ["capability manifest must be a JSON object"];
  }
  const errors = [];
  if (manifest.schemaVersion !== 1) {
    errors.push("capability manifest schemaVersion must be 1");
  }
  const manifestFields = new Set(["schemaVersion", "capabilities"]);
  const unknownManifestField = Object.keys(manifest).find((field) => !manifestFields.has(field));
  if (unknownManifestField) {
    errors.push(
      `capability manifest contains private or unknown field: ${unknownManifestField}`,
    );
  }
  if (!Array.isArray(manifest.capabilities) || manifest.capabilities.length === 0) {
    return [...errors, "capability manifest must declare at least one capability"];
  }

  const ids = new Set();
  for (const [index, capability] of manifest.capabilities.entries()) {
    const prefix = `capabilities[${index}]`;
    if (!capability || Array.isArray(capability) || typeof capability !== "object") {
      errors.push(`${prefix} must be an object`);
      continue;
    }
    if (typeof capability.id !== "string" || !/^[a-z][a-z0-9.-]+$/.test(capability.id)) {
      errors.push(`${prefix}.id must be a stable dotted identifier`);
    } else if (ids.has(capability.id)) {
      errors.push(`${prefix}.id duplicates ${capability.id}`);
    } else {
      ids.add(capability.id);
    }
    for (const field of ["category", "title"]) {
      if (typeof capability[field] !== "string" || !capability[field].trim()) {
        errors.push(`${prefix}.${field} must be a non-empty string`);
      }
    }
    if (!["absent", "implemented"].includes(capability.designState)) {
      errors.push(`${prefix}.designState must distinguish absent from implemented source`);
    }
    if (!CAPABILITY_RESPONSIBILITIES.has(capability.responsibility)) {
      errors.push(`${prefix}.responsibility must name starter, provider, adopter, or shared`);
    }
    if (!capability.configuration || typeof capability.configuration !== "object") {
      errors.push(`${prefix}.configuration must be an object`);
    } else {
      for (const field of ["providers", "settings"]) {
        const values = capability.configuration[field];
        if (!Array.isArray(values) || values.some((value) => typeof value !== "string" || !value.trim())) {
          errors.push(`${prefix}.configuration.${field} must be an array of non-empty strings`);
        } else if (new Set(values).size !== values.length) {
          errors.push(`${prefix}.configuration.${field} contains duplicates`);
        }
      }
    }
    if (!capability.public || typeof capability.public !== "object") {
      errors.push(`${prefix}.public must define the gated public summary`);
    } else {
      if (!CAPABILITY_STATES.includes(capability.public.minimumState)) {
        errors.push(`${prefix}.public.minimumState is not a capability state`);
      }
      if (typeof capability.public.summary !== "string" || !capability.public.summary.trim()) {
        errors.push(`${prefix}.public.summary must be a non-empty string`);
      }
    }
  }

  return errors;
}

export function capabilityContextErrors(context, manifest) {
  if (!context || Array.isArray(context) || typeof context !== "object") {
    return ["capability context must be a JSON object"];
  }
  const errors = [];
  const contextFields = new Set([
    "schemaVersion",
    "environment",
    "scope",
    "configuredProviders",
    "configuredSettings",
    "evidence",
  ]);
  const unknownContextField = Object.keys(context).find((field) => !contextFields.has(field));
  if (unknownContextField) {
    errors.push(`capability context contains unknown field: ${unknownContextField}`);
  }
  if (context.schemaVersion !== 1) {
    errors.push("capability context schemaVersion must be 1");
  }
  for (const field of ["environment", "scope"]) {
    if (typeof context[field] !== "string" || !context[field].trim()) {
      errors.push(`capability context ${field} must be a non-empty string`);
    }
  }
  for (const field of ["configuredProviders", "configuredSettings"]) {
    const values = context[field];
    if (!Array.isArray(values) || values.some((value) => typeof value !== "string" || !value.trim())) {
      errors.push(`capability context ${field} must be an array of non-empty strings`);
    } else if (new Set(values).size !== values.length) {
      errors.push(`capability context ${field} contains duplicates`);
    }
  }
  if (!Array.isArray(context.evidence)) {
    return [...errors, "capability context evidence must be an array"];
  }
  const ids = new Set(
    Array.isArray(manifest?.capabilities)
      ? manifest.capabilities.map((capability) => capability.id)
      : [],
  );
  const evidenceIDs = new Set();
  for (const [index, record] of context.evidence.entries()) {
    const prefix = `evidence[${index}]`;
    if (!record || Array.isArray(record) || typeof record !== "object") {
      errors.push(`${prefix} must be an object`);
      continue;
    }
    for (const field of [
      "id",
      "capabilityId",
      "environment",
      "scope",
      "owner",
      "verifier",
      "source",
      "performedAt",
      "reviewAt",
    ]) {
      if (typeof record[field] !== "string" || !record[field].trim()) {
        errors.push(`${prefix}.${field} must be a non-empty string`);
      }
    }
    if (evidenceIDs.has(record.id)) {
      errors.push(`${prefix}.id duplicates ${record.id}`);
    }
    evidenceIDs.add(record.id);
    if (!ids.has(record.capabilityId)) {
      errors.push(`${prefix}.capabilityId does not reference a declared capability`);
    }
    if (!EVIDENCE_STATES.has(record.state)) {
      errors.push(`${prefix}.state must be operationally_verified or externally_attested`);
    }
    if (!EVIDENCE_STATUSES.has(record.status)) {
      errors.push(`${prefix}.status is not supported`);
    }
    if (!EVIDENCE_VISIBILITIES.has(record.visibility)) {
      errors.push(`${prefix}.visibility must be private or public_summary`);
    }
    if (
      record.visibility === "public_summary" &&
      (typeof record.publicSummary !== "string" || !record.publicSummary.trim())
    ) {
      errors.push(`${prefix}.publicSummary is required for public_summary evidence`);
    }
    const performedAt = Date.parse(record.performedAt);
    const reviewAt = Date.parse(record.reviewAt);
    const expiresAt = record.expiresAt === undefined
      ? Number.POSITIVE_INFINITY
      : Date.parse(record.expiresAt);
    if (!Number.isFinite(performedAt)) errors.push(`${prefix}.performedAt must be an ISO timestamp`);
    if (!Number.isFinite(reviewAt)) errors.push(`${prefix}.reviewAt must be an ISO timestamp`);
    if (record.expiresAt !== undefined && !Number.isFinite(expiresAt)) {
      errors.push(`${prefix}.expiresAt must be an ISO timestamp when present`);
    }
    if (Number.isFinite(performedAt) && Number.isFinite(reviewAt) && reviewAt <= performedAt) {
      errors.push(`${prefix}.reviewAt must be after performedAt`);
    }
    if (Number.isFinite(performedAt) && Number.isFinite(expiresAt) && expiresAt <= performedAt) {
      errors.push(`${prefix}.expiresAt must be after performedAt`);
    }
  }
  return errors;
}

function canonicalClaimRoot(moduleRoot) {
  const repositoryRoot = dirname(moduleRoot);
  if (
    resolve(join(repositoryRoot, "module")) === resolve(moduleRoot) &&
    existsSync(join(repositoryRoot, "workspace.codefly.yaml"))
  ) {
    return repositoryRoot;
  }
  return moduleRoot;
}

function publicClaimFiles(moduleRoot, claimRoot) {
  const modulePrefix = relative(claimRoot, moduleRoot);
  const asModulePath = (rel) => {
    if (!modulePrefix) return rel;
    return rel.startsWith(`${modulePrefix}/`) ? rel.slice(modulePrefix.length + 1) : null;
  };
  return walk(claimRoot, [], claimRoot).filter((rel) => {
    if (/(?:^|\/)(?:__tests__|test|tests|gen|tools)(?:\/|$)/.test(rel)) return false;
    if (/\.test\.(?:[cm]?[jt]sx?)$/.test(rel)) return false;
    if (/\.(?:md|mdx|svg)$/.test(rel)) return true;

    const moduleRel = asModulePath(rel);
    if (!moduleRel) return false;
    if (moduleRel === CAPABILITY_MANIFEST_REL) return true;
    if (
      moduleRel.startsWith("services/frontend/code/src/") &&
      /\.(?:[cm]?[jt]sx?)$/.test(moduleRel)
    ) {
      return true;
    }
    if (
      (moduleRel.startsWith("fixtures/") ||
        moduleRel.includes("/fixtures/") ||
        moduleRel.startsWith("services/frontend/code/public/")) &&
      /\.(?:html|json|txt|ya?ml)$/.test(moduleRel)
    ) {
      return true;
    }
    return false;
  });
}

export function productionTruthErrors(
  moduleRoot = MODULE_ROOT,
  claimRoot = canonicalClaimRoot(moduleRoot),
) {
  const errors = [];
  const manifestPath = join(moduleRoot, CAPABILITY_MANIFEST_REL);
  if (!existsSync(manifestPath)) {
    const composed = composedServices(moduleRoot);
    if (!composed || composed.has("frontend")) {
      errors.push(`missing machine-readable capability manifest: ${CAPABILITY_MANIFEST_REL}`);
    }
  } else {
    try {
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      errors.push(...capabilityManifestErrors(manifest));
    } catch (error) {
      errors.push(`capability manifest is not valid JSON: ${error.message}`);
    }
  }

  for (const rel of publicClaimFiles(moduleRoot, claimRoot)) {
    const source = readFileSync(join(claimRoot, rel), "utf8");
    for (const [claim, pattern] of UNSUPPORTED_PUBLIC_CLAIMS) {
      if (pattern.test(source)) errors.push(`${rel}: unsupported ${claim}`);
    }
  }
  return errors;
}

// A consumer may compose a SUBSET of the base's services (e.g. mind takes the
// backend — api/store/vault/cache/object-storage — and brings its own gateway, so
// it omits auth-gateway + the frontend console). Files under an omitted service's
// directory are then legitimately absent and must NOT count as "missing". The
// composed set is the `services:` list in module.codefly.yaml; null = enforce
// everything (canonical itself, or a consumer with no explicit list).
function composedServices(moduleRoot = MODULE_ROOT) {
  const p = join(moduleRoot, "module.codefly.yaml");
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

// Re-derive the manifest a fresh `gen` would write for `moduleRoot`, without touching disk.
// `gen` persists this; the release gate compares it against the committed manifest.
export function computeBaseManifest(moduleRoot = MODULE_ROOT) {
  const files = walk(moduleRoot, [], moduleRoot).sort();
  const hashes = {};
  for (const rel of files) hashes[rel] = sha(join(moduleRoot, rel));
  return { note: MANIFEST_NOTE, fileCount: files.length, files: hashes };
}

// The canonical release gate: the committed manifest must equal a fresh regeneration of the
// tree — every field `gen` writes, so a passing `verify` proves `gen` is a no-op. `check`
// re-hashes only the paths already in the manifest, so a base file changed without a `gen`
// (v0.0.32: deployment_topology.go / network-policy.golden.yaml) sails through it — the stale
// digest is exactly what `check` trusts. Comparing against a fresh recomputation catches changed,
// unrecorded, and removed base files, plus a fileCount or note that drifted from the tree.
// Canonical-only: a consumer legitimately adds files, so this must never run against a consumer tree.
export function baseManifestFreshnessErrors(moduleRoot = MODULE_ROOT) {
  const manifestPath = join(moduleRoot, "tools", "base-manifest.json");
  if (!existsSync(manifestPath)) {
    return ["tools/base-manifest.json is missing — run `node tools/base-integrity.mjs gen`."];
  }
  let committed;
  try {
    committed = JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    return [`tools/base-manifest.json is not valid JSON: ${error.message}`];
  }
  const fresh = computeBaseManifest(moduleRoot);
  const committedFiles = committed.files ?? {};
  const errors = [];
  for (const [rel, want] of Object.entries(fresh.files)) {
    if (!(rel in committedFiles)) errors.push(`unrecorded base file: ${rel}`);
    else if (committedFiles[rel] !== want) errors.push(`stale hash: ${rel}`);
  }
  for (const rel of Object.keys(committedFiles)) {
    if (!(rel in fresh.files)) errors.push(`manifest lists a removed file: ${rel}`);
  }
  if (committed.fileCount !== fresh.fileCount) {
    errors.push(`fileCount ${committed.fileCount} does not match ${fresh.fileCount} base files`);
  }
  if (committed.note !== fresh.note) {
    errors.push("note does not match the canonical manifest note");
  }
  return errors;
}

function gen() {
  const truthErrors = productionTruthErrors();
  if (truthErrors.length) {
    truthErrors.forEach((error) => console.error(`production-truth: ${error}`));
    process.exit(1);
  }
  const installGraphErrors = workspaceInstallGraphErrors();
  if (installGraphErrors.length) {
    installGraphErrors.forEach((error) => console.error(`base-integrity: ${error}`));
    process.exit(1);
  }
  const migrationErrors = migrationPairingErrors();
  if (migrationErrors.length) {
    migrationErrors.forEach((error) => console.error(`migration-pairing-gate: ${error}`));
    process.exit(1);
  }
  const rlsErrors = rlsGateErrors();
  if (rlsErrors.length) {
    rlsErrors.forEach((error) => console.error(`rls-migration-gate: ${error}`));
    process.exit(1);
  }
  const manifest = computeBaseManifest();
  writeFileSync(MANIFEST_PATH, JSON.stringify(manifest, null, 2) + "\n");
  console.log(`base-integrity: wrote ${manifest.fileCount} base-file hashes to tools/base-manifest.json`);
}

// verify is the canonical release gate (Base manifest integrity CI job, on
// pull requests, pushes to main, and release tags). baseManifestFreshnessErrors
// catches drift in every HASHED base file; the migration, RLS, and
// production-truth gates read only hashed inputs, so a violation there changes a
// file hash, trips freshness, and is already blocked — they need not be repeated
// here. The frontend package-lock.json is the SOLE base file excluded from the
// hash manifest (it is regenerated per consumer), so its drift changes no hash
// and is invisible to freshness. verify must therefore validate it semantically,
// exactly as gen and check do, or a lock out of sync with the protected
// package.json and packages/* workspaces (the failure a downstream `npm ci`
// hits) ships silently. Any future base file added to isExcludedFile that a gate
// validates must be added below for the same reason. verifyErrors is the single
// enumerated list of what verify enforces, kept pure so tests can exercise it.
export function verifyErrors(moduleRoot = MODULE_ROOT) {
  return [
    {
      message:
        "base-manifest.json does not match the canonical tree — regenerate it "
        + "with `node tools/base-integrity.mjs gen` and commit the result:",
      errors: baseManifestFreshnessErrors(moduleRoot),
    },
    {
      message:
        "frontend package-lock.json is out of sync with the protected "
        + "package.json and packages/* workspaces — regenerate it with "
        + "`npm install --prefix services/frontend/code` and commit the result:",
      errors: workspaceInstallGraphErrors(
        join(moduleRoot, "services", "frontend", "code"),
      ),
    },
  ];
}

function verify() {
  const failed = verifyErrors().filter((group) => group.errors.length);
  if (failed.length) {
    for (const group of failed) {
      console.error(`base-integrity: ${group.message}`);
      group.errors.forEach((error) => console.error(`    ${error}`));
    }
    process.exit(1);
  }
  const { fileCount } = JSON.parse(readFileSync(MANIFEST_PATH, "utf8"));
  console.log(
    `✓ base-manifest.json matches the canonical tree (${fileCount} base files); `
    + "frontend workspace install graph is in sync.",
  );
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
  const migrationErrors = migrationPairingErrors();
  const rlsErrors = rlsGateErrors();
  const additionErrors = requiredAdditionsErrors(MODULE_ROOT, allow);
  const truthErrors = productionTruthErrors();

  console.log(`base-integrity: ${Object.keys(files).length} base files, ${additions.length} side-additions.`);
  if (badMissing.length) { console.error(`\n✗ MISSING base files (do not delete base files):`); badMissing.forEach((r) => console.error(`    ${r}`)); }
  if (badModified.length) { console.error(`\n✗ MODIFIED base files (add on the side, never edit the base):`); badModified.forEach((r) => console.error(`    ${r}`)); }
  if (installGraphErrors.length) { console.error(`\n✗ INVALID frontend workspace install graph:`); installGraphErrors.forEach((error) => console.error(`    ${error}`)); }
  if (migrationErrors.length) { console.error(`\n✗ ORPHANED OR DUPLICATED migration versions (see tools/migration-pairing-gate.mjs):`); migrationErrors.forEach((error) => console.error(`    ${error}`)); }
  if (rlsErrors.length) { console.error(`\n✗ UNPROTECTED tenant-scoped tables (see tools/rls-migration-gate.mjs):`); rlsErrors.forEach((error) => console.error(`    ${error}`)); }
  if (additionErrors.length) { console.error(`\n✗ MISSING OR INVALID required consumer additions:`); additionErrors.forEach((error) => console.error(`    ${error}`)); }
  if (truthErrors.length) { console.error(`\n✗ INVALID production capability claims:`); truthErrors.forEach((error) => console.error(`    ${error}`)); }

  if (badModified.length || badMissing.length || installGraphErrors.length || migrationErrors.length || rlsErrors.length || additionErrors.length || truthErrors.length) {
    console.error(`\nFAIL: ${badModified.length} modified, ${badMissing.length} missing, ${installGraphErrors.length} invalid install-graph checks, ${migrationErrors.length} orphaned/duplicated migration-version checks, ${rlsErrors.length} unprotected tenant-table checks, ${additionErrors.length} invalid required-addition checks, ${truthErrors.length} invalid production-truth checks. `
      + `Move your change upstream into canonical (making the original stronger), or express it as a side-addition.`);
    process.exit(1);
  }
  console.log("✓ base intact — every base file matches canonical; all consumer changes are additions.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "gen") gen();
  else if (cmd === "check") check();
  else if (cmd === "verify") verify();
  else { console.error("usage: base-integrity.mjs <gen|check|verify>"); process.exit(2); }
}
