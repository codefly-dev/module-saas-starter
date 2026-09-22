import assert from "node:assert/strict";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  capabilityContextErrors,
  capabilityManifestErrors,
  productionTruthErrors,
  verifyErrors,
  satisfiesWorkspaceRange,
  workspaceInstallGraphErrors,
  workspaceLinkSatisfactionErrors,
} from "./module-verify.mjs";

function writeJSON(path, value) {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`);
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "saas-workspace-integrity-"));
  const packageRoot = join(root, "packages", "product-plugin");
  mkdirSync(packageRoot, { recursive: true });
  const rootManifest = {
    name: "frontend",
    version: "1.0.0",
    workspaces: ["packages/*"],
    dependencies: { react: "19.2.4" },
  };
  const productManifest = {
    name: "@product/frontend-plugin",
    version: "1.0.0",
    dependencies: { "@codefly/saas-plugin-contract": "1.2.0" },
    peerDependencies: { react: ">=19.2 <20" },
  };
  const lock = {
    name: "frontend",
    version: "1.0.0",
    lockfileVersion: 3,
    packages: {
      "": rootManifest,
      "node_modules/@product/frontend-plugin": {
        resolved: "packages/product-plugin",
        link: true,
      },
      "packages/product-plugin": productManifest,
    },
  };
  writeJSON(join(root, "package.json"), rootManifest);
  writeJSON(join(packageRoot, "package.json"), productManifest);
  writeJSON(join(root, "package-lock.json"), lock);
  return { root, packageRoot, rootManifest, productManifest, lock };
}

test("accepts an exact additive packages/* install graph", (t) => {
  const { root } = fixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  assert.deepEqual(workspaceInstallGraphErrors(root), []);
});

// The committed lock is the one the release archive ships and every downstream
// `npm ci` installs from. `verify` runs this same check, so a stale lock here

test("committed frontend package-lock.json is in sync with its workspaces", () => {
  const frontendCodeRoot = join(
    dirname(fileURLToPath(import.meta.url)),
    "..",
    "services",
    "frontend",
    "code",
  );
  // workspaceInstallGraphErrors returns [] when both manifest and lock are
  // absent, so assert the files exist first — otherwise a moved or renamed
  // frontend would let this test pass vacuously while asserting nothing.
  assert.ok(existsSync(join(frontendCodeRoot, "package.json")));
  assert.ok(existsSync(join(frontendCodeRoot, "package-lock.json")));
  assert.deepEqual(workspaceInstallGraphErrors(frontendCodeRoot), []);
});

// verify's checks live in one enumerated function; this proves it enforces the
// unhashed frontend lock, not merely the hash manifest — the drift the hash
// gate is blind to and the exact failure #359 shipped. The frontend is nested at
// services/frontend/code because verifyErrors takes a module root, not a

// verify's checks live in one enumerated function; this proves it enforces the
// frontend lock — the drift the exact failure #359 shipped. The frontend is
// nested at services/frontend/code because verifyErrors takes a module root.
test("verifyErrors enforces the frontend lock", (t) => {
  const moduleRoot = mkdtempSync(join(tmpdir(), "saas-module-verify-"));
  t.after(() => rmSync(moduleRoot, { recursive: true, force: true }));
  const frontendCodeRoot = join(moduleRoot, "services", "frontend", "code");
  const packageRoot = join(frontendCodeRoot, "packages", "product-plugin");
  mkdirSync(packageRoot, { recursive: true });
  const rootManifest = {
    name: "frontend",
    version: "1.0.0",
    workspaces: ["packages/*"],
    dependencies: { react: "19.2.4" },
  };
  const productManifest = {
    name: "@product/frontend-plugin",
    version: "1.0.0",
    dependencies: { "@codefly/saas-plugin-contract": "1.2.0" },
    peerDependencies: { react: ">=19.2 <20" },
  };
  // A lock missing the workspace link — the stale shape #359 shipped.
  const lock = {
    name: "frontend",
    version: "1.0.0",
    lockfileVersion: 3,
    packages: { "": rootManifest, "packages/product-plugin": productManifest },
  };
  writeJSON(join(frontendCodeRoot, "package.json"), rootManifest);
  writeJSON(join(packageRoot, "package.json"), productManifest);
  writeJSON(join(frontendCodeRoot, "package-lock.json"), lock);
  const errors = verifyErrors(moduleRoot).flatMap((group) => group.errors);
  assert.ok(errors.some((error) => error.includes("workspace link")));
});

test("rejects stale product metadata and missing workspace links", (t) => {
  const { root, packageRoot, productManifest, lock } = fixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));

  writeJSON(join(packageRoot, "package.json"), {
    ...productManifest,
    dependencies: { "@codefly/saas-plugin-contract": "2.0.0" },
  });
  assert.ok(
    workspaceInstallGraphErrors(root).some((error) => error.includes("metadata is stale")),
  );

  writeJSON(join(packageRoot, "package.json"), productManifest);
  delete lock.packages["node_modules/@product/frontend-plugin"];
  writeJSON(join(root, "package-lock.json"), lock);
  assert.ok(
    workspaceInstallGraphErrors(root).some((error) => error.includes("workspace link")),
  );
});

test("rejects lock entries for removed workspaces", (t) => {
  const { root, packageRoot } = fixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  rmSync(packageRoot, { recursive: true });
  assert.ok(
    workspaceInstallGraphErrors(root).some((error) => error.includes("missing or removed")),
  );
});

test("rejects a missing lock beside an installed frontend", (t) => {
  const { root } = fixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  unlinkSync(join(root, "package-lock.json"));
  assert.deepEqual(workspaceInstallGraphErrors(root), [
    "frontend package-lock.json is missing beside package.json",
  ]);
});

test("rejects every local Codefly SDK dependency", (t) => {
  const { root, rootManifest, lock } = fixture();
  t.after(() => rmSync(root, { recursive: true, force: true }));
  rootManifest.dependencies.codefly = "file:../../../../../../codefly/sdk-js";
  lock.packages[""].dependencies.codefly = "file:../../../../../../codefly/sdk-js";
  writeJSON(join(root, "package.json"), rootManifest);
  writeJSON(join(root, "package-lock.json"), lock);
  assert.ok(
    workspaceInstallGraphErrors(root).some((error) =>
      error.includes("must use a published version")),
  );

  rootManifest.dependencies.codefly = "file:../../../../../../sdk-js";
  lock.packages[""].dependencies.codefly = "file:../../../../../../sdk-js";
  writeJSON(join(root, "package.json"), rootManifest);
  writeJSON(join(root, "package-lock.json"), lock);
  assert.ok(
    workspaceInstallGraphErrors(root).some((error) =>
      error.includes("must use a published version")),
  );
});

function capabilityManifest() {
  return {
    schemaVersion: 1,
    capabilities: [{
      id: "operations.backup-restore",
      category: "Operations",
      title: "Backup and restore",
      designState: "absent",
      responsibility: "shared",
      configuration: {
        providers: ["backup-provider"],
        settings: ["recovery.policy"],
      },
      public: {
        minimumState: "operationally_verified",
        summary: "Recovery has current scoped exercise evidence.",
      },
    }],
  };
}

function capabilityContext() {
  return {
    schemaVersion: 1,
    environment: "production",
    scope: "primary region",
    configuredProviders: ["backup-provider"],
    configuredSettings: ["recovery.policy"],
    evidence: [],
  };
}

test("keeps private evidence out of the public capability manifest", () => {
  const manifest = capabilityManifest();
  manifest.evidence = [];
  assert.ok(
    capabilityManifestErrors(manifest).some((error) =>
      error.includes("private or unknown field: evidence")),
  );
});

test("validates complete private capability context records", () => {
  const manifest = capabilityManifest();
  const context = capabilityContext();
  context.evidence.push({
    id: "ev-restore",
    capabilityId: "operations.backup-restore",
    environment: "production",
    scope: "primary region",
    owner: "operations",
    verifier: "release-manager",
    source: "private://recovery/restore-exercise",
    performedAt: "2026-07-20T10:00:00Z",
    reviewAt: "2026-08-20T10:00:00Z",
    expiresAt: "2026-10-20T10:00:00Z",
    status: "current",
    state: "operationally_verified",
    visibility: "private",
  });
  assert.deepEqual(capabilityContextErrors(context, manifest), []);
});

test("rejects incomplete, orphaned, or unsafe public evidence metadata", () => {
  const manifest = capabilityManifest();
  const context = capabilityContext();
  context.evidence.push({
    id: "ev-orphan",
    capabilityId: "missing.capability",
    environment: "production",
    scope: "primary region",
    owner: "operations",
    verifier: "release-manager",
    source: "private://recovery/restore-exercise",
    performedAt: "invalid",
    reviewAt: "2026-07-19T10:00:00Z",
    status: "current",
    state: "implemented",
    visibility: "public_summary",
  });
  const errors = capabilityContextErrors(context, manifest);
  assert.ok(errors.some((error) => error.includes("does not reference")));
  assert.ok(errors.some((error) => error.includes("operationally_verified")));
  assert.ok(errors.some((error) => error.includes("publicSummary")));
  assert.ok(errors.some((error) => error.includes("performedAt")));
});

test("rejects unsupported promises in customer-visible source", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-production-truth-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const manifestPath = join(
    root,
    "services/frontend/code/src/features/trust/capability-manifest.json",
  );
  const pagePath = join(
    root,
    "services/frontend/code/src/app/docs/compliance/page.tsx",
  );
  mkdirSync(join(manifestPath, ".."), { recursive: true });
  mkdirSync(join(pagePath, ".."), { recursive: true });
  writeJSON(manifestPath, capabilityManifest());
  writeFileSync(
    pagePath,
    `export default function Page() {
      return [
        "Backups are retained for 90 days",
        "production-grade from day one",
        "Trusted by teams shipping",
        "SOC 2 / enterprise-compliant path",
        "tamper-evident copy outside the platform",
      ];
    }\n`,
  );
  writeFileSync(
    join(root, "OBSERVABILITY.md"),
    "Applications continue to\nsend OTLP through OTEL_EXPORTER_OTLP_ENDPOINT.\n",
  );

  const errors = productionTruthErrors(root);
  assert.ok(errors.includes("OBSERVABILITY.md: unsupported direct application OTLP endpoint"));
  for (const claim of [
    "unsupported fixed backup retention",
    "unsupported production readiness",
    "unverified customer endorsement",
    "unsupported assurance path",
    "unverified audit immutability",
  ]) {
    assert.ok(errors.some((error) => error.includes(claim)));
  }
});

test("scans public manifest summaries, repository docs, nested docs, and fixtures", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-production-truth-repository-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const moduleRoot = join(root, "module");
  const manifestPath = join(
    moduleRoot,
    "services/frontend/code/src/features/trust/capability-manifest.json",
  );
  const nestedDoc = join(moduleRoot, "deployment", "README.md");
  const fixturePath = join(moduleRoot, "fixtures", "customer.json");
  mkdirSync(join(manifestPath, ".."), { recursive: true });
  mkdirSync(join(nestedDoc, ".."), { recursive: true });
  mkdirSync(join(fixturePath, ".."), { recursive: true });

  const manifest = capabilityManifest();
  manifest.capabilities[0].public.summary = "Backups are retained for 90 days";
  writeJSON(manifestPath, manifest);
  writeFileSync(join(root, "PRODUCTION_READY.md"), "production-grade from day one\n");
  writeFileSync(nestedDoc, "Trusted by teams shipping\n");
  writeJSON(fixturePath, { claim: "SOC 2 / enterprise-compliant path" });

  const errors = productionTruthErrors(moduleRoot, root);
  for (const path of [
    "module/services/frontend/code/src/features/trust/capability-manifest.json",
    "PRODUCTION_READY.md",
    "module/deployment/README.md",
    "module/fixtures/customer.json",
  ]) {
    assert.ok(errors.some((error) => error.startsWith(`${path}:`)), path);
  }
});


test("does not require the frontend capability manifest when frontend is omitted", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-production-truth-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  writeFileSync(
    join(root, "module.codefly.yaml"),
    "name: consumer\nservices:\n  - name: accounts\n",
  );

  assert.deepEqual(productionTruthErrors(root), []);
});

// The base-file set is git's index, not the working tree: a build product the
// release engineer's .gitignore hides must never enter the manifest, because it
// becomes a base file every consumer is missing and none can restore.

test("satisfiesWorkspaceRange evaluates the ranges workspace links use", () => {
  assert.equal(satisfiesWorkspaceRange("0.2.1", "0.2.1"), true);
  assert.equal(satisfiesWorkspaceRange("0.2.0", "0.2.1"), false);
  assert.equal(satisfiesWorkspaceRange("^0.2.1", "0.2.9"), true);
  // ^0.2.1 must NOT allow 0.3.0: in a 0.x line npm treats a minor as breaking.
  assert.equal(satisfiesWorkspaceRange("^0.2.1", "0.3.0"), false);
  assert.equal(satisfiesWorkspaceRange("^0.0.3", "0.0.4"), false);
  assert.equal(satisfiesWorkspaceRange("^0", "0.9.9"), true);
  assert.equal(satisfiesWorkspaceRange("^0", "1.0.0"), false);
  assert.equal(satisfiesWorkspaceRange("^1.2.3", "1.9.9"), true);
  assert.equal(satisfiesWorkspaceRange("^1.2.3", "2.0.0"), false);
  assert.equal(satisfiesWorkspaceRange("~0.2.1", "0.2.9"), true);
  assert.equal(satisfiesWorkspaceRange("~0.2.1", "0.3.0"), false);
  assert.equal(satisfiesWorkspaceRange("~1.2", "1.2.9"), true);
  assert.equal(satisfiesWorkspaceRange("~1.2", "1.3.0"), false);
  assert.equal(satisfiesWorkspaceRange(">=19.2 <20", "19.2.8"), true);
  assert.equal(satisfiesWorkspaceRange(">=19.2 <20", "20.0.0"), false);
  assert.equal(satisfiesWorkspaceRange("^1.0.0 || ^2.0.0", "2.1.0"), true);
});

// Forms npm accepts that an earlier revision of this gate rejected outright,
// hard-failing "Base manifest integrity" and telling the author their perfectly
// legal range was unsupported.
test("satisfiesWorkspaceRange accepts the npm range forms it once false-rejected", () => {
  // Operator detached from its operand.
  assert.equal(satisfiesWorkspaceRange(">= 0.2.1", "0.2.1"), true);
  assert.equal(satisfiesWorkspaceRange(">=  0.2.1  <0.4.0", "0.3.0"), true);
  assert.equal(satisfiesWorkspaceRange("<  0.2.1", "0.3.0"), false);
  // Leading `v`.
  assert.equal(satisfiesWorkspaceRange("v0.2.1", "0.2.1"), true);
  // X-ranges and wildcards.
  assert.equal(satisfiesWorkspaceRange("0.2.x", "0.2.9"), true);
  assert.equal(satisfiesWorkspaceRange("0.2.x", "0.3.0"), false);
  assert.equal(satisfiesWorkspaceRange("1.x", "1.9.9"), true);
  assert.equal(satisfiesWorkspaceRange("1.x", "2.0.0"), false);
  assert.equal(satisfiesWorkspaceRange("*", "9.9.9"), true);
  assert.equal(satisfiesWorkspaceRange("x", "1.0.0"), true);
});

// npm excludes a prerelease from a range unless a comparator pins the same
// major.minor.patch AND carries a prerelease itself. Getting this wrong in the
// permissive direction would call an unlinkable workspace fine.
test("satisfiesWorkspaceRange applies npm's prerelease inclusion rule", () => {
  assert.equal(satisfiesWorkspaceRange("^1.0.0", "1.5.0-rc.1"), false);
  assert.equal(satisfiesWorkspaceRange(">=0.3.0-rc.1", "0.3.0-rc.2"), true);
  assert.equal(satisfiesWorkspaceRange("^0.3.0-rc.1", "0.3.0-rc.2"), true);
  assert.equal(satisfiesWorkspaceRange("0.3.0-rc.1", "0.3.0-rc.1"), true);
  // Prerelease sorts below its own release.
  assert.equal(satisfiesWorkspaceRange(">=0.3.0", "0.3.0-rc.1"), false);
});

// Fail closed: a shape the evaluator does not understand must surface as unknown
// (null) so the caller errors, never as a silent pass.
test("satisfiesWorkspaceRange reports unknown rather than guessing", () => {
  for (const range of ["1.0.0 - 2.0.0", "workspace:*", ">=1.0.0 || ", "not-a-range"]) {
    assert.equal(satisfiesWorkspaceRange(range, "1.0.0"), null, range);
  }
});

// The regression this gate exists for: `@codefly-dev/saas-ui` kept requiring
// `@codefly-dev/saas-sdk@0.2.0` after the SDK workspace moved to 0.2.1. The
// lockfile agreed with the manifest, so every metadata-equality check passed and
// "Base manifest integrity" reported in-sync — while `npm ci` went to the public
// registry for a package that is not there and failed three CI jobs with E404.
test("workspaceLinkSatisfactionErrors catches a workspace pin the workspace cannot satisfy", () => {
  const workspaces = [
    { label: "packages/saas-sdk/package.json", manifest: { name: "@codefly-dev/saas-sdk", version: "0.2.1" } },
    {
      label: "packages/saas-ui/package.json",
      manifest: {
        name: "@codefly-dev/saas-ui",
        version: "0.2.0",
        devDependencies: { "@codefly-dev/saas-sdk": "0.2.0" },
        peerDependencies: { "@codefly-dev/saas-sdk": "0.2.0", react: ">=19.2 <20" },
      },
    },
  ];
  const errors = workspaceLinkSatisfactionErrors({ workspaces });
  assert.equal(errors.length, 2);
  for (const error of errors) {
    assert.match(error, /@codefly-dev\/saas-sdk = "0\.2\.0" is not satisfied by workspace @codefly-dev\/saas-sdk@0\.2\.1/);
  }
  // A third-party range is not a workspace edge and must not be evaluated.
  assert.ok(!errors.some((error) => error.includes("react")));
});

test("workspaceLinkSatisfactionErrors accepts a range the workspace satisfies", () => {
  assert.deepEqual(
    workspaceLinkSatisfactionErrors({
      workspaces: [
        { label: "packages/saas-sdk/package.json", manifest: { name: "@codefly-dev/saas-sdk", version: "0.2.1" } },
        {
          label: "packages/saas-ui/package.json",
          manifest: {
            name: "@codefly-dev/saas-ui",
            version: "0.2.0",
            peerDependencies: { "@codefly-dev/saas-sdk": "^0.2.1" },
          },
        },
      ],
    }),
    [],
  );
});

// Scope check, verified against real npm: a ROOT dependency resolves a workspace
// by NAME and links it whatever the range says — even when the registry carries
// the pinned version (a workspace named `is-odd` at 99.0.0 wins over a root pin
// of the real `is-odd@3.0.1`). So a drifting root pin is not an install hazard,
// and an earlier revision that flagged it failed the repo-wide gate with an E404
// claim that could never happen.
test("workspaceLinkSatisfactionErrors does not police root-manifest pins", () => {
  const workspaces = [
    { label: "packages/saas-sdk/package.json", manifest: { name: "@codefly-dev/saas-sdk", version: "0.2.2" } },
  ];
  // Passed the way the caller once did; the root manifest must be ignored.
  assert.deepEqual(workspaceLinkSatisfactionErrors({ workspaces }), []);
  assert.deepEqual(
    workspaceLinkSatisfactionErrors({
      root: { dependencies: { "@codefly-dev/saas-sdk": "0.2.1" } },
      workspaces,
    }),
    [],
  );
});

// A sibling that declares no readable `version` produces the SAME E404 as a
// mismatched range (reproduced against real npm), so it must not be dropped.
// An earlier revision skipped it entirely and returned no errors at all.
test("workspaceLinkSatisfactionErrors catches an edge onto an unusable workspace version", () => {
  for (const declared of [undefined, "not-a-version", null]) {
    const manifest = { name: "foo" };
    if (declared !== undefined) manifest.version = declared;
    const errors = workspaceLinkSatisfactionErrors({
      workspaces: [
        { label: "packages/foo/package.json", manifest },
        {
          label: "packages/bar/package.json",
          manifest: { name: "bar", version: "1.0.0", devDependencies: { foo: "1.0.0" } },
        },
      ],
    });
    assert.equal(errors.length, 1, `declared=${String(declared)}`);
    assert.match(errors[0], /points at workspace packages\/foo\/package\.json/);
    assert.match(errors[0], /public registry instead of the local workspace/);
  }
});

// …but a workspace nobody depends on cannot break an install, so it is not an
// error on its own. Over-reporting here would fail the repo-wide gate for a
// private helper package that is perfectly fine.
test("workspaceLinkSatisfactionErrors ignores an unusable version nothing depends on", () => {
  assert.deepEqual(
    workspaceLinkSatisfactionErrors({
      workspaces: [{ label: "packages/foo/package.json", manifest: { name: "foo" } }],
    }),
    [],
  );
});

test("workspaceLinkSatisfactionErrors supports a prerelease workspace version", () => {
  const workspaces = (range) => [
    { label: "packages/a/package.json", manifest: { name: "a", version: "0.3.0-rc.1" } },
    { label: "packages/b/package.json", manifest: { name: "b", version: "1.0.0", dependencies: { a: range } } },
  ];
  assert.deepEqual(workspaceLinkSatisfactionErrors({ workspaces: workspaces("^0.3.0-rc.1") }), []);
  const stale = workspaceLinkSatisfactionErrors({ workspaces: workspaces("^0.1.0") });
  assert.equal(stale.length, 1);
  assert.match(stale[0], /is not satisfied by workspace a@0\.3\.0-rc\.1/);
});

test("workspaceLinkSatisfactionErrors fails closed on an unevaluatable workspace range", () => {
  const errors = workspaceLinkSatisfactionErrors({
    workspaces: [
      { label: "packages/saas-ui/package.json", manifest: { name: "@codefly-dev/saas-ui", version: "1.5.0" } },
      {
        label: "packages/other/package.json",
        manifest: { name: "other", version: "1.0.0", dependencies: { "@codefly-dev/saas-ui": "1.0.0 - 2.0.0" } },
      },
    ],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /cannot evaluate/);
});
