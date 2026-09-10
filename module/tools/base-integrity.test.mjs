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
  baseManifestFreshnessErrors,
  capabilityContextErrors,
  capabilityManifestErrors,
  computeBaseManifest,
  isExcludedFile,
  productionTruthErrors,
  requiredAdditionsErrors,
  verifyErrors,
  satisfiesWorkspaceRange,
  workspaceInstallGraphErrors,
  workspaceLinkSatisfactionErrors,
} from "./base-integrity.mjs";

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
// fails the base-integrity gate before it can reach a consumer.
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
// frontend root.
test("verifyErrors enforces the excluded frontend lock", (t) => {
  const moduleRoot = mkdtempSync(join(tmpdir(), "saas-module-integrity-"));
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

test("excludes compiled service binaries without excluding their source", () => {
  assert.equal(isExcludedFile("services/store/code/store-migrator"), true);
  assert.equal(isExcludedFile("services/store/code/main.go"), false);
  assert.equal(isExcludedFile("services/store/migrations/1_create.up.sql"), false);
});

test("excludes per-service Nix runtime directories from the canonical base", () => {
  assert.equal(
    isExcludedFile("services/vault/.nix-cache/nix-devshell-profile-1-link"),
    true,
  );
  assert.equal(isExcludedFile("services/vault/nix/service.nix"), true);
  assert.equal(isExcludedFile("services/vault/code/service.go"), false);
});

test("excludes runtime-owned secret configuration from the canonical base", () => {
  assert.equal(
    isExcludedFile("services/store/configurations/local/postgres.secret.env"),
    true,
  );
  assert.equal(
    isExcludedFile("services/store/configurations/local/postgres.env"),
    false,
  );
  // Workspace-config-group secrets ship at the module root too; exclude them on
  // the same runtime-owned grounds, while non-secret group defaults stay canonical.
  assert.equal(
    isExcludedFile("configurations/local/internal-auth.secret.env"),
    true,
  );
  assert.equal(isExcludedFile("configurations/local/legal.env"), false);
});

test("excludes consumer-generated module and GitOps manifests", () => {
  assert.equal(isExcludedFile("module.codefly.yaml"), true);
  assert.equal(isExcludedFile("deployment/generated/service-topology.json"), true);
  assert.equal(isExcludedFile("deployment/kustomize/inventory.json"), true);
  assert.equal(
    isExcludedFile("deployment/kustomize/overlays/aws/applications/accounts.yaml"),
    true,
  );
  assert.equal(isExcludedFile("deployment/topology.bindings.codefly.yaml"), false);
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

test("requires consumer-owned composition files without hashing their contents", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-required-additions-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const required = "services/frontend/code/packages/product-plugin/package.json";
  const allow = { requiredAdditions: { [required]: "installs the product UI" } };

  assert.deepEqual(requiredAdditionsErrors(root, allow), [
    `required consumer addition is missing: ${required}`,
  ]);
  mkdirSync(join(root, "services/frontend/code/packages/product-plugin"), { recursive: true });
  writeFileSync(join(root, required), "{}\n");
  assert.deepEqual(requiredAdditionsErrors(root, allow), []);
});

test("rejects unsafe or undocumented required additions", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-required-additions-policy-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));

  assert.deepEqual(requiredAdditionsErrors(root, { requiredAdditions: [] }), [
    "base-integrity-allow.json requiredAdditions must be a path-to-reason object",
  ]);
  assert.ok(requiredAdditionsErrors(root, {
    requiredAdditions: { "../outside": "must not escape", "inside": "" },
  }).some((error) => error.includes("escapes the module")));
  assert.ok(requiredAdditionsErrors(root, {
    requiredAdditions: { "inside": "" },
  }).some((error) => error.includes("non-empty reason")));
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

test("the committed canonical manifest matches the tree it ships with", () => {
  // This is the release gate: had it run for v0.0.32, the stale digests for
  // deployment_topology.go and network-policy.golden.yaml would have failed here.
  assert.deepEqual(baseManifestFreshnessErrors(), []);
});

test("flags changed, unrecorded, and removed base files against a fresh regeneration", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-manifest-freshness-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "nested"), { recursive: true });
  mkdirSync(join(root, "tools"), { recursive: true });
  writeFileSync(join(root, "a.txt"), "alpha\n");
  writeFileSync(join(root, "nested/b.txt"), "bravo\n");

  const manifestPath = join(root, "tools/base-manifest.json");
  writeJSON(manifestPath, computeBaseManifest(root));
  assert.deepEqual(baseManifestFreshnessErrors(root), []);

  const stale = computeBaseManifest(root);
  stale.files["a.txt"] = "0".repeat(64); // v0.0.32-style: file changed, manifest not regenerated
  delete stale.files["nested/b.txt"]; // base file present on disk but absent from the manifest
  stale.files["gone.txt"] = "0".repeat(64); // manifest entry with no file behind it
  writeJSON(manifestPath, stale);

  assert.deepEqual(baseManifestFreshnessErrors(root).sort(), [
    "manifest lists a removed file: gone.txt",
    "stale hash: a.txt",
    "unrecorded base file: nested/b.txt",
  ]);
});

test("flags a fileCount or note that drifts even when every hash is current", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-manifest-metadata-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "tools"), { recursive: true });
  writeFileSync(join(root, "a.txt"), "alpha\n");
  const manifestPath = join(root, "tools/base-manifest.json");

  // Correct hashes, but the recorded fileCount and note no longer match what
  // `gen` would write — the artifact is stale even though `check` would pass.
  const drifted = computeBaseManifest(root);
  drifted.fileCount = 999;
  drifted.note = "hand-edited note";
  writeJSON(manifestPath, drifted);

  const errors = baseManifestFreshnessErrors(root);
  assert.ok(errors.some((error) => error === "fileCount 999 does not match 1 base files"));
  assert.ok(errors.some((error) => error === "note does not match the canonical manifest note"));
  assert.ok(!errors.some((error) => error.startsWith("stale hash")));
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

// The range evaluation is hand-rolled (bare node, no `semver`), and it sits on a
// gate that runs for every PR in the repo — so a FALSE REJECT is as damaging as a
// false accept: it hard-fails unrelated PRs until someone edits a range npm was
// always happy with. These pin both directions.
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
  const errors = workspaceLinkSatisfactionErrors({ root: {}, workspaces });
  assert.equal(errors.length, 2);
  for (const error of errors) {
    assert.match(error, /@codefly-dev\/saas-sdk = "0\.2\.0" is not satisfied by workspace @codefly-dev\/saas-sdk@0\.2\.1/);
  }
  // A third-party range is not a workspace edge and must not be evaluated.
  assert.ok(!errors.some((error) => error.includes("react")));
});

test("workspaceLinkSatisfactionErrors accepts a range the workspace satisfies", () => {
  const workspaces = [
    { label: "packages/saas-sdk/package.json", manifest: { name: "@codefly-dev/saas-sdk", version: "0.2.1" } },
    {
      label: "packages/saas-ui/package.json",
      manifest: {
        name: "@codefly-dev/saas-ui",
        version: "0.2.0",
        peerDependencies: { "@codefly-dev/saas-sdk": "^0.2.1" },
      },
    },
  ];
  assert.deepEqual(
    workspaceLinkSatisfactionErrors({
      root: { dependencies: { "@codefly-dev/saas-ui": "0.2.0" } },
      workspaces,
    }),
    [],
  );
});

// A prerelease workspace version is a normal thing to cut. It must not be
// reported as an unevaluatable RANGE — that names the wrong file, and an earlier
// revision did exactly that for every dependent edge.
test("workspaceLinkSatisfactionErrors supports a prerelease workspace version", () => {
  const workspaces = [
    { label: "packages/a/package.json", manifest: { name: "a", version: "0.3.0-rc.1" } },
  ];
  assert.deepEqual(
    workspaceLinkSatisfactionErrors({ root: { dependencies: { a: "^0.3.0-rc.1" } }, workspaces }),
    [],
  );
  const stale = workspaceLinkSatisfactionErrors({
    root: { dependencies: { a: "^0.1.0" } },
    workspaces,
  });
  assert.equal(stale.length, 1);
  assert.match(stale[0], /is not satisfied by workspace a@0\.3\.0-rc\.1/);
});

// An unreadable VERSION is reported against the workspace that declares it, not
// blamed on whichever range happened to reference it.
test("workspaceLinkSatisfactionErrors blames an unreadable version on its own workspace", () => {
  const errors = workspaceLinkSatisfactionErrors({
    root: { dependencies: { a: "^1.0.0" } },
    workspaces: [{ label: "packages/a/package.json", manifest: { name: "a", version: "not-a-version" } }],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /packages\/a\/package\.json version "not-a-version" is not a semver/);
  assert.ok(!errors[0].includes("^1.0.0"));
});

test("workspaceLinkSatisfactionErrors fails closed on an unevaluatable workspace range", () => {
  const errors = workspaceLinkSatisfactionErrors({
    root: { dependencies: { "@codefly-dev/saas-ui": "1.0.0 - 2.0.0" } },
    workspaces: [
      { label: "packages/saas-ui/package.json", manifest: { name: "@codefly-dev/saas-ui", version: "1.5.0" } },
    ],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /cannot evaluate/);
});
