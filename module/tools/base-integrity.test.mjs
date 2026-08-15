import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  baseManifestFreshnessErrors,
  capabilityContextErrors,
  capabilityManifestErrors,
  computeBaseManifest,
  isExcludedFile,
  migrationVersionErrors,
  productionTruthErrors,
  requiredAdditionsErrors,
  workspaceInstallGraphErrors,
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

test("excludes compiled service binaries without excluding their source", () => {
  assert.equal(isExcludedFile("services/store/code/store-migrator"), true);
  assert.equal(isExcludedFile("services/store/code/main.go"), false);
  assert.equal(isExcludedFile("services/store/migrations/1_create.up.sql"), false);
});

test("excludes per-service Nix caches from the canonical package", () => {
  assert.equal(
    isExcludedFile("services/vault/.nix-cache/nix-devshell-profile-1-link"),
    true,
  );
  assert.equal(isExcludedFile("services/vault/nix/service.nix"), false);
});

test("rejects multiple logical migrations with the same version", (t) => {
  const root = mkdtempSync(join(tmpdir(), "saas-migration-integrity-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const migrations = join(root, "services", "store", "migrations");
  mkdirSync(migrations, { recursive: true });
  for (const name of [
    "78_authorization_revisions.up.sql",
    "78_authorization_revisions.down.sql",
    "78_inbound_forge_edge.up.sql",
    "78_inbound_forge_edge.down.sql",
  ]) {
    writeFileSync(join(migrations, name), "-- migration\n");
  }

  assert.deepEqual(migrationVersionErrors(root), [
    "store migration version 78 is used by multiple migrations: authorization_revisions, inbound_forge_edge",
  ]);
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

  const errors = productionTruthErrors(root);
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
