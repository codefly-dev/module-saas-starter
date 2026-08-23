import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  auditCoverageErrors,
  broadeningViolations,
  loadAllowlist,
  loadCatalog,
  rbacCoverageErrors,
} from "./authz-coverage-gate.mjs";

// A minimal well-formed method the tests mutate per case.
function method(overrides = {}) {
  const { policy = {}, ...rest } = overrides;
  return {
    procedure: "/saas.accounts.v1.DemoService/Do",
    method: "Do",
    policy_tier: "auth",
    policy: {
      exposure: "EXPOSURE_AUTHENTICATED",
      tenant: "TENANT_REQUIREMENT_USER",
      mfa: "MFA_REQUIREMENT_NONE",
      platform_role: "PLATFORM_ROLE_REQUIREMENT_NONE",
      permissions: [],
      scopes: [],
      audit: { emission: "AUDIT_EMISSION_NONE", events: [] },
      ...policy,
    },
    ...rest,
  };
}

// --- the shipped catalog must satisfy every gate ---------------------------

test("the committed catalog passes the rbac gate", () => {
  assert.deepEqual(rbacCoverageErrors(loadCatalog(), loadAllowlist().rbac), []);
});

test("the committed catalog passes the audit gate", () => {
  assert.deepEqual(auditCoverageErrors(loadCatalog(), loadAllowlist().audit), []);
});

test("the committed catalog does not broaden against itself", () => {
  assert.deepEqual(broadeningViolations(loadCatalog(), loadCatalog()), []);
});

// --- rbac coverage ----------------------------------------------------------

test("an unspecified exposure has no declared gate", () => {
  const errors = rbacCoverageErrors([method({ policy: { exposure: "EXPOSURE_UNSPECIFIED" } })]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /no declared exposure gate/);
});

test("an unknown policy tier fails", () => {
  const errors = rbacCoverageErrors([method({ policy_tier: "wizard" })]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /unknown policy tier wizard/);
});

test("a tier served on the wrong exposure fails", () => {
  const errors = rbacCoverageErrors([method({ policy_tier: "public" })]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /must be served on EXPOSURE_PUBLIC/);
});

test("a platform_admin route without a platform-role requirement fails", () => {
  const errors = rbacCoverageErrors([
    method({ policy_tier: "platform_admin", policy: { platform_role: "PLATFORM_ROLE_REQUIREMENT_NONE" } }),
  ]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /declares no platform-role requirement/);
});

test("a platform-role requirement on a non-platform_admin tier fails", () => {
  const errors = rbacCoverageErrors([
    method({ policy_tier: "auth", policy: { platform_role: "PLATFORM_ROLE_REQUIREMENT_SUPPORT" } }),
  ]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /non-platform_admin tier/);
});

test("a ticketed rbac allowlist entry suppresses the failure", () => {
  const bad = method({ policy: { exposure: "EXPOSURE_UNSPECIFIED" } });
  const allow = new Map([[bad.procedure, { reason: "x", ticket: "#1" }]]);
  assert.deepEqual(rbacCoverageErrors([bad], allow), []);
});

// --- audit coverage ---------------------------------------------------------

test("a mutating authenticated route with no audit fails", () => {
  const errors = auditCoverageErrors([method({ procedure: "/s/CreateThing", method: "CreateThing" })]);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /mutating route emits no audit/);
});

test("a mutating route that emits audit passes", () => {
  const errors = auditCoverageErrors([
    method({ method: "CreateThing", policy: { audit: { emission: "AUDIT_EMISSION_SUCCESS", events: ["thing.created"] } } }),
  ]);
  assert.deepEqual(errors, []);
});

test("a non-mutating read is not required to audit", () => {
  assert.deepEqual(auditCoverageErrors([method({ method: "ListThings" })]), []);
});

test("an internal mutating route is out of audit scope", () => {
  const errors = auditCoverageErrors([
    method({ method: "ConsumeUsage", policy_tier: "internal", policy: { exposure: "EXPOSURE_INTERNAL" } }),
  ]);
  assert.deepEqual(errors, []);
});

test("a ticketed audit allowlist entry suppresses the failure", () => {
  const bad = method({ procedure: "/s/DeleteThing", method: "DeleteThing" });
  const allow = new Map([[bad.procedure, { reason: "x", ticket: "#1" }]]);
  assert.deepEqual(auditCoverageErrors([bad], allow), []);
});

// --- no-broadening diff -----------------------------------------------------

test("lowering exposure from authenticated to public is a broadening", () => {
  const base = [method()];
  const head = [method({ policy_tier: "public", policy: { exposure: "EXPOSURE_PUBLIC", tenant: "TENANT_REQUIREMENT_NONE" } })];
  const violations = broadeningViolations(base, head);
  assert.match(violations.join("\n"), /exposure weakened EXPOSURE_AUTHENTICATED → EXPOSURE_PUBLIC/);
});

test("relaxing the tenant requirement is a broadening", () => {
  const base = [method({ policy: { tenant: "TENANT_REQUIREMENT_ORG_ADMIN" } })];
  const head = [method({ policy: { tenant: "TENANT_REQUIREMENT_ORG_MEMBER" } })];
  assert.match(broadeningViolations(base, head).join("\n"), /tenant weakened .*ORG_ADMIN → .*ORG_MEMBER/);
});

test("dropping a required permission is a broadening", () => {
  const base = [method({ policy: { permissions: ["users:write"] } })];
  const head = [method({ policy: { permissions: [] } })];
  assert.match(broadeningViolations(base, head).join("\n"), /permissions requirement dropped: users:write/);
});

test("tightening a route is not a broadening", () => {
  const base = [method({ policy: { tenant: "TENANT_REQUIREMENT_ORG_MEMBER", permissions: [] } })];
  const head = [method({ policy: { tenant: "TENANT_REQUIREMENT_ORG_ADMIN", permissions: ["users:write"] } })];
  assert.deepEqual(broadeningViolations(base, head), []);
});

test("a route absent from head cannot broaden", () => {
  assert.deepEqual(broadeningViolations([method()], []), []);
});

test("lowering the mfa requirement is a broadening despite the enum order", () => {
  const base = [method({ policy: { mfa: "MFA_REQUIREMENT_RECENT_STEP_UP" } })];
  const head = [method({ policy: { mfa: "MFA_REQUIREMENT_IF_ENROLLED_RECENT_STEP_UP" } })];
  assert.match(broadeningViolations(base, head).join("\n"), /mfa weakened/);
});

// --- CLI exit codes and approval-label semantics ---------------------------

const GATE = join(dirname(fileURLToPath(import.meta.url)), "authz-coverage-gate.mjs");

function runGate(args, env = {}) {
  return spawnSync(process.execPath, [GATE, ...args], { env: { ...process.env, ...env }, encoding: "utf8" });
}

// A base catalog that broadens the committed head: an internal route relaxed to
// public. Written to a temp file so the diff is head-vs-a-known-narrower-base.
function narrowerBaseCatalog() {
  const methods = loadCatalog().map((m) => ({ ...m }));
  const target = methods.find((m) => m.policy?.exposure === "EXPOSURE_AUTHENTICATED");
  target.policy = { ...target.policy, permissions: [...(target.policy.permissions ?? []), "sentinel:write"] };
  const path = join(mkdtempSync(join(tmpdir(), "authz-base-")), "authz-methods.json");
  writeFileSync(path, JSON.stringify({ methods }, null, 2));
  return path;
}

test("check exits 0 on the committed catalog", () => {
  assert.equal(runGate(["check"]).status, 0);
});

test("no-broadening fails when a required permission is dropped and no label is set", () => {
  const result = runGate(["no-broadening", "--base", narrowerBaseCatalog()]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /permissions requirement dropped: sentinel:write/);
});

test("the literal string 'false' does not approve a broadening", () => {
  const result = runGate(["no-broadening", "--base", narrowerBaseCatalog()], { AUTHZ_ALLOW_BROADENING: "false" });
  assert.equal(result.status, 1);
});

test("an affirmative approval token allows the broadening", () => {
  const result = runGate(["no-broadening", "--base", narrowerBaseCatalog()], { AUTHZ_ALLOW_BROADENING: "true" });
  assert.equal(result.status, 0);
});

test("no-broadening is a no-op when the base catalog is absent", () => {
  assert.equal(runGate(["no-broadening", "--base", "/does/not/exist.json"]).status, 0);
});
