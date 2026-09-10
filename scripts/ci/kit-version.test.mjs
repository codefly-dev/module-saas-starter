import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { newestBaseline, PUBLISHED_KIT_PACKAGES, staleVersionErrors } from "./kit-version.mjs";
import { PACKAGES } from "../../module/services/frontend/code/scripts/publish-frontend-kit.mjs";

const REPOSITORY_ROOT = join(import.meta.dirname, "..", "..");
const BASELINE = "v0.0.58";

const kitPackage = (overrides) => ({
  name: "@codefly-dev/ui",
  changed: true,
  version: "0.1.0",
  baselineVersion: "0.1.0",
  ...overrides,
});

// ---------------------------------------------------------------------------
// staleVersionErrors — the rule itself
// ---------------------------------------------------------------------------

// The v0.0.58 failure, caught one step earlier: changed bytes, untouched version.
test("changed content under the baseline's version is rejected", () => {
  assert.deepEqual(staleVersionErrors({ baseline: BASELINE, packages: [kitPackage()] }), [
    "@codefly-dev/ui: its content changed since v0.0.58 but its version is still 0.1.0",
  ]);
});

test("changed content with a bumped version is accepted", () => {
  const packages = [kitPackage({ version: "0.2.0" })];
  assert.deepEqual(staleVersionErrors({ baseline: BASELINE, packages }), []);
});

// A release that didn't touch the kit republishes nothing, so the version it
// already carries stays correct.
test("an unchanged package needs no bump", () => {
  const packages = [kitPackage({ changed: false })];
  assert.deepEqual(staleVersionErrors({ baseline: BASELINE, packages }), []);
});

test("a package that did not exist at the baseline has nothing to contradict", () => {
  const packages = [kitPackage({ baselineVersion: null })];
  assert.deepEqual(staleVersionErrors({ baseline: BASELINE, packages }), []);
});

test("each package is judged on its own", () => {
  const packages = [
    kitPackage({ version: "0.2.0" }),
    kitPackage({ name: "@codefly-dev/saas-sdk", version: "0.2.0", baselineVersion: "0.2.0" }),
  ];
  assert.deepEqual(staleVersionErrors({ baseline: BASELINE, packages }), [
    "@codefly-dev/saas-sdk: its content changed since v0.0.58 but its version is still 0.2.0",
  ]);
});

// ---------------------------------------------------------------------------
// newestBaseline — which release the content is compared against
// ---------------------------------------------------------------------------

test("the newest release tag is the baseline", () => {
  const tags = ["v0.0.59", "v0.0.58", "v0.0.57"];
  assert.equal(newestBaseline({ tags, tagsAtHead: [] }), "v0.0.59");
});

// On a release run HEAD is the tag being released. Comparing it against itself
// would report no change and gate nothing, so the predecessor is the baseline.
test("a tag pointing at HEAD is skipped", () => {
  const tags = ["v0.0.59", "v0.0.58"];
  assert.equal(newestBaseline({ tags, tagsAtHead: ["v0.0.59"] }), "v0.0.58");
});

test("no reachable release tag leaves no baseline", () => {
  assert.equal(newestBaseline({ tags: [], tagsAtHead: [] }), null);
  assert.equal(newestBaseline({ tags: ["v0.0.59"], tagsAtHead: ["v0.0.59"] }), null);
});

// ---------------------------------------------------------------------------
// PUBLISHED_KIT_PACKAGES — the gate's coverage of the publish set
// ---------------------------------------------------------------------------

// The publish script decides what reaches the registry; this gate decides what
// must be bumped first. A package added there without a directory here would be
// published unchecked.
test("the gate covers exactly the packages the release publishes", () => {
  assert.deepEqual(
    PUBLISHED_KIT_PACKAGES.map(({ name }) => name).sort(),
    [...PACKAGES].sort(),
  );
});

test("each declared directory holds the package it claims", () => {
  for (const { name, directory } of PUBLISHED_KIT_PACKAGES) {
    const manifest = JSON.parse(
      readFileSync(join(REPOSITORY_ROOT, directory, "package.json"), "utf8"),
    );
    assert.equal(manifest.name, name);
  }
});
