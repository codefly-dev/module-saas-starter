import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import { applyPlan, assertApplicable, buildPlan } from "./base-sync.mjs";

const hash = (value) => createHash("sha256").update(value).digest("hex");
const write = (root, relative, value) => {
  const path = join(root, relative);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, value);
};
const manifest = (root, files) => write(root, "tools/base-manifest.json", `${JSON.stringify({ files }, null, 2)}\n`);

test("manifest sync preserves allowed files and detects every overwrite class", () => {
  const sandbox = mkdtempSync(join(tmpdir(), "saas-base-sync-"));
  const source = join(sandbox, "source");
  const target = join(sandbox, "target");

  write(source, "base.txt", "canonical-new");
  write(source, "new-base.txt", "canonical-collision");
  write(source, "released.bin", "canonical-build-artifact");
  write(source, "module.codefly.yaml", "canonical-module");
  manifest(source, {
    "base.txt": hash("canonical-new"),
    "new-base.txt": hash("canonical-collision"),
    "module.codefly.yaml": hash("canonical-module"),
  });

  write(target, "base.txt", "canonical-old");
  write(target, "new-base.txt", "consumer-side-addition");
  write(target, "stale.txt", "old-base-file");
  write(target, "released.bin", "consumer-build-artifact");
  write(target, "module.codefly.yaml", "consumer-module");
  write(target, "tools/base-integrity-allow.json", `${JSON.stringify({
    "module.codefly.yaml": "consumer identity",
  })}\n`);
  manifest(target, {
    "base.txt": hash("canonical-old"),
    "stale.txt": hash("old-base-file"),
    "released.bin": hash("old-canonical-build-artifact"),
    "module.codefly.yaml": hash("canonical-module"),
  });

  const options = {
    target,
    apply: true,
    replaceModified: false,
    replaceCollisions: true,
  };
  const plan = buildPlan(options, source);
  assert.deepEqual(plan.update, ["base.txt"]);
  assert.deepEqual(plan.collision, ["new-base.txt"]);
  assert.deepEqual(plan.remove, ["stale.txt"]);
  assert.deepEqual(plan.released, ["released.bin"]);
  assert.deepEqual(plan.allowed, ["module.codefly.yaml"]);
  assert.doesNotThrow(() => assertApplicable(plan, options));

  applyPlan(plan, options, source);
  assert.equal(readFileSync(join(target, "base.txt"), "utf8"), "canonical-new");
  assert.equal(readFileSync(join(target, "new-base.txt"), "utf8"), "canonical-collision");
  assert.equal(readFileSync(join(target, "module.codefly.yaml"), "utf8"), "consumer-module");
  assert.equal(readFileSync(join(target, "released.bin"), "utf8"), "consumer-build-artifact");
  assert.equal(readFileSync(join(target, "tools/base-manifest.json"), "utf8"), readFileSync(join(source, "tools/base-manifest.json"), "utf8"));
});

test("sync refuses to mutate a consumer whose required overlay is missing", () => {
  const sandbox = mkdtempSync(join(tmpdir(), "saas-base-sync-required-overlay-"));
  const source = join(sandbox, "source");
  const target = join(sandbox, "target");

  write(source, "base.txt", "canonical-new");
  manifest(source, { "base.txt": hash("canonical-new") });
  write(target, "base.txt", "canonical-old");
  manifest(target, { "base.txt": hash("canonical-old") });
  write(target, "tools/base-integrity-allow.json", `${JSON.stringify({
    requiredAdditions: {
      "services/frontend/code/packages/product/package.json": "product UI package",
    },
  })}\n`);

  const options = { target, apply: true, replaceModified: false, replaceCollisions: false };
  const plan = buildPlan(options, source);
  assert.deepEqual(plan.requiredAdditionErrors, [
    "required consumer addition is missing: services/frontend/code/packages/product/package.json",
  ]);
  assert.throws(() => assertApplicable(plan, options), /required consumer additions/);
});
