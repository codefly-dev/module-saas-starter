#!/usr/bin/env node
// authz-coverage-gate — default-deny CI gates over the generated authorization
// catalog (services/accounts/generated/authz-methods.json).
//
// The module ships a complete, machine-readable policy for every RPC, but until
// now nothing failed the build when a new route slipped through with no gate, a
// state-changing route emitted no audit, or a change silently widened who may
// call an existing route. The generator validates a policy in isolation; these
// gates assert coverage and monotonicity across the whole catalog and against
// `main`, so an ungated or broadened route is un-mergeable rather than merely
// discouraged. Failing closed is the point — a route with an under-specified
// gate is more dangerous than a red build.
//
//   node tools/authz-coverage-gate.mjs rbac            # every RPC declares a coherent gate
//   node tools/authz-coverage-gate.mjs audit           # every mutating RPC emits audit
//   node tools/authz-coverage-gate.mjs check           # rbac + audit
//   node tools/authz-coverage-gate.mjs no-broadening --base <authz-methods.json>
//                                                      # reject gates that widen vs the base
//
// Ticketed exemptions live in tools/authz-coverage-allowlist.json. The module
// root is the parent of tools/, so this works identically in canonical's
// `module/` and a consumer's `modules/<name>/`.

import { readFileSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");
const CATALOG_PATH = join(MODULE_ROOT, "services", "accounts", "generated", "authz-methods.json");
const ALLOWLIST_PATH = join(MODULE_ROOT, "tools", "authz-coverage-allowlist.json");

// The trust boundary a route is exposed on. A route with any other exposure has
// no declared gate.
const VALID_EXPOSURES = new Set(["EXPOSURE_PUBLIC", "EXPOSURE_AUTHENTICATED", "EXPOSURE_INTERNAL"]);

// The compatibility/policy tier and the exposure it must be served on. A tier
// outside this map, or one paired with the wrong exposure, is an incoherent gate.
const TIER_EXPOSURE = new Map([
  ["public", "EXPOSURE_PUBLIC"],
  ["internal", "EXPOSURE_INTERNAL"],
  ["auth", "EXPOSURE_AUTHENTICATED"],
  ["mfa", "EXPOSURE_AUTHENTICATED"],
  ["org_member", "EXPOSURE_AUTHENTICATED"],
  ["org_admin", "EXPOSURE_AUTHENTICATED"],
  ["platform_admin", "EXPOSURE_AUTHENTICATED"],
]);

const NO_PLATFORM_ROLE = "PLATFORM_ROLE_REQUIREMENT_NONE";

// Method-name prefixes that denote a state-changing write. The audit gate is
// deliberately broad here: a mutation-shaped route must either emit audit or
// carry a ticketed exemption, so a genuinely non-mutating false match costs one
// reviewed allowlist entry while a real mutation can never pass unexamined.
const MUTATING_VERBS = [
  "Accept", "Add", "Assign", "Begin", "Cancel", "Complete", "Consume", "Create",
  "Decide", "Delete", "Disable", "Enable", "Exchange", "Finish", "Generate",
  "Grant", "Impersonate", "Invite", "Join", "Mark", "Override", "Reevaluate",
  "Refresh", "Register", "Remove", "Replay", "Request", "Resend", "Reset",
  "Revoke", "Rotate", "Save", "Set", "Setup", "Share", "Skip", "Start",
  "Suspend", "Switch", "Unsuspend", "Update", "Upsert", "Verify",
];
const MUTATING_RE = new RegExp(`^(?:${MUTATING_VERBS.join("|")})`);

// Audit coverage applies to caller-attributable exposures. Internal
// service-to-service routes are audited by the authenticated edge call that
// drives them, not by the internal hop itself.
const AUDITABLE_EXPOSURES = new Set(["EXPOSURE_PUBLIC", "EXPOSURE_AUTHENTICATED"]);

// --- rank tables for the no-broadening diff ---------------------------------
// Each table orders a policy dimension from broadest (0) to most restrictive.
// A change that lowers a route's rank on any dimension widens access.

const EXPOSURE_RANK = new Map([
  ["EXPOSURE_PUBLIC", 0],
  ["EXPOSURE_AUTHENTICATED", 1],
  ["EXPOSURE_INTERNAL", 2],
]);

const TENANT_RANK = new Map([
  ["TENANT_REQUIREMENT_NONE", 0],
  ["TENANT_REQUIREMENT_USER", 1],
  ["TENANT_REQUIREMENT_TEAM_MEMBER", 1],
  ["TENANT_REQUIREMENT_ORG_MEMBER", 2],
  ["TENANT_REQUIREMENT_ORG_ADMIN", 3],
  ["TENANT_REQUIREMENT_ORG_OWNER", 4],
]);

const PLATFORM_ROLE_RANK = new Map([
  ["PLATFORM_ROLE_REQUIREMENT_NONE", 0],
  ["PLATFORM_ROLE_REQUIREMENT_ANY", 1],
  ["PLATFORM_ROLE_REQUIREMENT_SUPPORT", 2],
  ["PLATFORM_ROLE_REQUIREMENT_BILLING", 3],
  ["PLATFORM_ROLE_REQUIREMENT_SUPER_ADMIN", 4],
]);

// MFA strength is not the proto enum order: IF_ENROLLED_RECENT_STEP_UP is weaker
// than an unconditional RECENT_STEP_UP, so it is ranked explicitly.
const MFA_RANK = new Map([
  ["MFA_REQUIREMENT_NONE", 0],
  ["MFA_REQUIREMENT_IF_ENROLLED_RECENT_STEP_UP", 1],
  ["MFA_REQUIREMENT_ENROLLED", 2],
  ["MFA_REQUIREMENT_RECENT_STEP_UP", 3],
]);

function readJSON(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

export function loadCatalog(path = CATALOG_PATH) {
  return readJSON(path).methods ?? [];
}

export function loadAllowlist(path = ALLOWLIST_PATH) {
  const raw = existsSync(path) ? readJSON(path) : {};
  return {
    rbac: normalizeAllowlist(raw.rbac),
    audit: normalizeAllowlist(raw.audit),
  };
}

// Index an allowlist section by procedure, keeping only entries that carry both
// a reason and a ticket. A malformed entry is ignored so it cannot silently
// exempt a route without a reviewable justification.
function normalizeAllowlist(section) {
  const byProcedure = new Map();
  for (const entry of section ?? []) {
    if (entry?.procedure && entry.reason && entry.ticket) {
      byProcedure.set(entry.procedure, entry);
    }
  }
  return byProcedure;
}

const isMutating = (method) => MUTATING_RE.test(method.method ?? "");

// --- RBAC coverage ----------------------------------------------------------

export function rbacCoverageErrors(methods, allowlist = new Map()) {
  const errors = [];
  for (const method of methods) {
    const procedure = method.procedure;
    if (allowlist.has(procedure)) continue;

    const exposure = method.policy?.exposure;
    const tier = method.policy_tier;

    if (!VALID_EXPOSURES.has(exposure)) {
      errors.push(`${procedure}: no declared exposure gate (exposure=${exposure ?? "<missing>"})`);
      continue;
    }
    if (!TIER_EXPOSURE.has(tier)) {
      errors.push(`${procedure}: unknown policy tier ${tier ?? "<missing>"}`);
      continue;
    }
    if (TIER_EXPOSURE.get(tier) !== exposure) {
      errors.push(
        `${procedure}: tier ${tier} must be served on ${TIER_EXPOSURE.get(tier)} but is exposed as ${exposure}`,
      );
    }
    const platformRole = method.policy?.platform_role ?? NO_PLATFORM_ROLE;
    if (tier === "platform_admin" && platformRole === NO_PLATFORM_ROLE) {
      errors.push(`${procedure}: platform_admin route declares no platform-role requirement`);
    }
    if (tier !== "platform_admin" && platformRole !== NO_PLATFORM_ROLE) {
      errors.push(`${procedure}: platform-role requirement ${platformRole} on non-platform_admin tier ${tier}`);
    }
  }
  return errors.sort();
}

// --- audit coverage ---------------------------------------------------------

export function auditCoverageErrors(methods, allowlist = new Map()) {
  const errors = [];
  for (const method of methods) {
    const exposure = method.policy?.exposure;
    if (!AUDITABLE_EXPOSURES.has(exposure)) continue;
    if (!isMutating(method)) continue;
    if (allowlist.has(method.procedure)) continue;

    const emission = method.policy?.audit?.emission;
    if (!emission || emission === "AUDIT_EMISSION_NONE") {
      errors.push(`${method.procedure}: mutating route emits no audit (audit.emission=${emission ?? "<missing>"})`);
    }
  }
  return errors.sort();
}

// --- no-broadening diff -----------------------------------------------------

// Return a widening reason for a single dimension, or null. A value absent from
// the rank table is treated as unranked and only compared for equality, so an
// unrecognized enum can never be read as a widening.
function widenedRank(label, table, base, head) {
  if (base === head) return null;
  const b = table.get(base);
  const h = table.get(head);
  if (b === undefined || h === undefined || h >= b) return null;
  return `${label} weakened ${base} → ${head}`;
}

// Required values dropped from base to head widen access (fewer requirements to
// satisfy). Added requirements narrow access and are allowed.
function droppedRequirements(label, base, head) {
  const headSet = new Set(head ?? []);
  const dropped = (base ?? []).filter((value) => !headSet.has(value));
  return dropped.length ? `${label} requirement dropped: ${dropped.sort().join(", ")}` : null;
}

export function broadeningViolations(baseMethods, headMethods) {
  const head = new Map(headMethods.map((m) => [m.procedure, m]));
  const violations = [];
  for (const baseMethod of baseMethods) {
    const headMethod = head.get(baseMethod.procedure);
    if (!headMethod) continue; // removed route cannot broaden

    const b = baseMethod.policy ?? {};
    const h = headMethod.policy ?? {};
    const reasons = [
      widenedRank("exposure", EXPOSURE_RANK, b.exposure, h.exposure),
      widenedRank("tenant", TENANT_RANK, b.tenant, h.tenant),
      widenedRank("platform_role", PLATFORM_ROLE_RANK, b.platform_role ?? NO_PLATFORM_ROLE, h.platform_role ?? NO_PLATFORM_ROLE),
      widenedRank("mfa", MFA_RANK, b.mfa, h.mfa),
      droppedRequirements("permissions", b.permissions, h.permissions),
      droppedRequirements("scopes", b.scopes, h.scopes),
    ].filter(Boolean);

    for (const reason of reasons) {
      violations.push(`${baseMethod.procedure}: ${reason}`);
    }
  }
  return violations.sort();
}

// --- CLI --------------------------------------------------------------------

// Print a gate's outcome and return whether it passed. It never exits, so a
// combined run (check) can report every gate's violations in one pass instead
// of stopping at the first failure.
function report(name, errors, okMessage, failMessage) {
  if (errors.length) {
    console.error(`authz-coverage-gate ${name}: ${failMessage}`);
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(`\nFAIL: ${errors.length} ${name} violation(s).`);
    return false;
  }
  console.log(okMessage);
  return true;
}

function runRbac(catalog, allowlist) {
  return report(
    "rbac",
    rbacCoverageErrors(catalog, allowlist.rbac),
    "✓ every RPC declares a coherent authorization gate.",
    "RPCs without a declared, coherent gate (add a policy or a ticketed allowlist entry):",
  );
}

function runAudit(catalog, allowlist) {
  return report(
    "audit",
    auditCoverageErrors(catalog, allowlist.audit),
    "✓ every mutating RPC emits audit or carries a ticketed exemption.",
    "mutating RPCs with no audit emission (emit audit or add a ticketed allowlist entry):",
  );
}

function runNoBroadening(argv) {
  const baseFlag = argv.indexOf("--base");
  if (baseFlag === -1 || !argv[baseFlag + 1]) {
    console.error("usage: authz-coverage-gate.mjs no-broadening --base <authz-methods.json> [--head <authz-methods.json>]");
    process.exit(2);
  }
  const basePath = argv[baseFlag + 1];
  if (!existsSync(basePath)) {
    console.log(`authz-coverage-gate no-broadening: no base catalog at ${basePath}; nothing to diff.`);
    return;
  }
  const headFlag = argv.indexOf("--head");
  const headMethods = headFlag === -1 ? loadCatalog() : loadCatalog(argv[headFlag + 1]);
  const violations = broadeningViolations(loadCatalog(basePath), headMethods);

  // The CI label check renders to the literal string "false" when absent, which
  // is truthy in JS, so an approval requires an explicit affirmative token.
  const approved = /^(1|true|yes)$/i.test(process.env.AUTHZ_ALLOW_BROADENING ?? "");

  if (violations.length && !approved) {
    console.error("authz-coverage-gate no-broadening: routes widen access versus the base:");
    violations.forEach((violation) => console.error(`    ${violation}`));
    console.error(
      `\nFAIL: ${violations.length} broadening change(s). Apply the approval label so CI sets ` +
        "AUTHZ_ALLOW_BROADENING, or narrow the change.",
    );
    process.exit(1);
  }
  if (violations.length) {
    console.warn(`authz-coverage-gate no-broadening: ${violations.length} broadening change(s) approved via label:`);
    violations.forEach((violation) => console.warn(`    ${violation}`));
    return;
  }
  console.log("✓ no route widens access versus the base.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "rbac" || cmd === "audit" || cmd === "check") {
    const catalog = loadCatalog();
    const allowlist = loadAllowlist();
    const rbacOK = cmd !== "audit" ? runRbac(catalog, allowlist) : true;
    const auditOK = cmd !== "rbac" ? runAudit(catalog, allowlist) : true;
    process.exit(rbacOK && auditOK ? 0 : 1);
  } else if (cmd === "no-broadening") runNoBroadening(process.argv.slice(3));
  else {
    console.error("usage: authz-coverage-gate.mjs <rbac|audit|check|no-broadening>");
    process.exit(2);
  }
}
