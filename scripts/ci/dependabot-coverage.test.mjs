import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  CONFIG_PATH,
  coverageErrors,
  dependabotCoverageErrors,
  dependabotEntries,
  discoverManifests,
  normalizeDirectory,
} from "./dependabot-coverage.mjs";

const entry = (ecosystem, ...directories) => ({ ecosystem, directories });
const manifest = (ecosystem, directory, file) => ({
  ecosystem,
  directory,
  path: directory ? `${directory}/${file}` : file,
});

const run = ({ entries = [], manifests = [], baseTracked = [], hasWorkflows = false }) =>
  coverageErrors({ entries, manifests, baseTracked: new Set(baseTracked), hasWorkflows });

// --------------------------------------------------------------------------
// the shipped tree
// --------------------------------------------------------------------------

test("the shipped dependabot configuration matches the tree", () => {
  assert.deepEqual(dependabotCoverageErrors(), []);
});

// --------------------------------------------------------------------------
// a base-tracked manifest must never be configured
// --------------------------------------------------------------------------

// The defect this gate exists for. Configuring npm over
// module/services/frontend/code bumps a package.json whose hash lives in
// base-manifest.json; `base-integrity` then fails and Dependabot cannot
// regenerate the manifest, so the pull request can never merge and the
// ecosystem's open-pull-requests-limit stays consumed by it.
test("configuring a base-tracked manifest is an error", () => {
  const errors = run({
    entries: [entry("npm", "module/services/frontend/code")],
    manifests: [manifest("npm", "module/services/frontend/code", "package.json")],
    baseTracked: ["module/services/frontend/code/package.json"],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /hashed in module\/tools\/base-manifest\.json/);
  assert.match(errors[0], /can never merge/);
});

test("a base-tracked manifest left unconfigured is not an error", () => {
  assert.deepEqual(
    run({
      manifests: [manifest("pip", "module/services/accounts/code/python", "pyproject.toml")],
      baseTracked: ["module/services/accounts/code/python/pyproject.toml"],
    }),
    [],
  );
});

test("every base-tracked manifest under one entry is reported, not just the first", () => {
  const errors = run({
    entries: [entry("gomod", "a", "b")],
    manifests: [manifest("gomod", "a", "go.mod"), manifest("gomod", "b", "go.mod")],
    baseTracked: ["a/go.mod", "b/go.mod"],
  });
  assert.equal(errors.length, 2);
});

// --------------------------------------------------------------------------
// an updatable manifest must never be left out
// --------------------------------------------------------------------------

// The mirror defect: the original configuration covered the repository-root
// go.mod and none of the five modules beneath it, which reports nothing at all.
test("a non-base-tracked manifest with no entry is an error", () => {
  const errors = run({
    entries: [entry("gomod", "")],
    manifests: [manifest("gomod", "", "go.mod"), manifest("gomod", "tools", "go.mod")],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /tools\/go\.mod is not base-tracked/);
  assert.match(errors[0], /no updates/);
});

test("a manifest covered by its own entry is not an error", () => {
  assert.deepEqual(
    run({
      entries: [entry("docker", "images")],
      manifests: [manifest("docker", "images", "Dockerfile")],
    }),
    [],
  );
});

// An entry for one ecosystem must not silence a different ecosystem sharing the
// directory, which is how a pyproject.toml beside a go.mod would slip through.
test("an entry covers only its own ecosystem in that directory", () => {
  const errors = run({
    entries: [entry("gomod", "svc")],
    manifests: [manifest("gomod", "svc", "go.mod"), manifest("pip", "svc", "pyproject.toml")],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /svc\/pyproject\.toml is not base-tracked/);
});

// --------------------------------------------------------------------------
// an entry must point at something
// --------------------------------------------------------------------------

test("an entry over a directory with no manifest of its ecosystem is an error", () => {
  const errors = run({
    entries: [entry("pip", "module/services/accounts/code/python")],
    manifests: [],
  });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /holds no pip manifest/);
  assert.match(errors[0], /can never open a pull request/);
});

// --------------------------------------------------------------------------
// github-actions, which has no manifest file
// --------------------------------------------------------------------------

test("github-actions is satisfied by the workflows directory, not by a manifest", () => {
  assert.deepEqual(run({ entries: [entry("github-actions", "")], hasWorkflows: true }), []);
});

test("github-actions outside the repository root is an error", () => {
  const errors = run({ entries: [entry("github-actions", ".github")], hasWorkflows: true });
  assert.equal(errors.length, 2);
  assert.match(errors[0], /requires the directory to be \//);
  assert.match(errors[1], /no github-actions entry covers \//);
});

test("workflows with no github-actions entry are an error", () => {
  const errors = run({ entries: [], hasWorkflows: true });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /no github-actions entry covers \//);
});

test("a github-actions entry with no workflows is an error", () => {
  const errors = run({ entries: [entry("github-actions", "")], hasWorkflows: false });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /holds no workflow/);
});

// --------------------------------------------------------------------------
// reading the configuration
// --------------------------------------------------------------------------

test("the singular and plural directory forms parse to the same shape", () => {
  const singular = dependabotEntries(
    ["version: 2", "updates:", "  - package-ecosystem: gomod", "    directory: /"].join("\n"),
  );
  const plural = dependabotEntries(
    [
      "version: 2",
      "updates:",
      "  - package-ecosystem: gomod",
      "    directories:",
      "      - /",
    ].join("\n"),
  );
  assert.deepEqual(singular, [{ ecosystem: "gomod", directories: [""] }]);
  assert.deepEqual(singular, plural);
});

test("an entry with no directory at all yields no coverage", () => {
  assert.deepEqual(
    dependabotEntries(["version: 2", "updates:", "  - package-ecosystem: gomod"].join("\n")),
    [{ ecosystem: "gomod", directories: [] }],
  );
});

test("normalizeDirectory spells the repository root as the empty string", () => {
  assert.equal(normalizeDirectory("/"), "");
  assert.equal(normalizeDirectory("/module/tools"), "module/tools");
  assert.equal(normalizeDirectory("/module/tools/"), "module/tools");
});

test("an unparseable configuration is one reported defect, not a silent pass", () => {
  const root = mkdtempSync(join(tmpdir(), "dependabot-coverage-"));
  mkdirSync(join(root, ".github"), { recursive: true });
  writeFileSync(join(root, ".github", "dependabot.yml"), "updates:\n  - nope\n   bad-indent: 1\n");
  const errors = dependabotCoverageErrors(root);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /could not be parsed/);
});

test("a missing configuration is reported rather than treated as empty", () => {
  const root = mkdtempSync(join(tmpdir(), "dependabot-coverage-"));
  const errors = dependabotCoverageErrors(root);
  assert.deepEqual(errors, [`${CONFIG_PATH} is missing under ${root}`]);
});

// --------------------------------------------------------------------------
// discovery
// --------------------------------------------------------------------------

test("discovery finds each ecosystem and skips installed trees", () => {
  const root = mkdtempSync(join(tmpdir(), "dependabot-discover-"));
  const write = (relative, body = "{}") => {
    mkdirSync(join(root, relative, ".."), { recursive: true });
    writeFileSync(join(root, relative), body);
  };
  mkdirSync(join(root, "svc"), { recursive: true });
  mkdirSync(join(root, "web", "node_modules", "left-pad"), { recursive: true });
  mkdirSync(join(root, "svc", "vendor", "dep"), { recursive: true });
  write("go.mod", "module x\n");
  write("svc/Dockerfile", "FROM alpine\n");
  write("svc/Dockerfile.debug", "FROM alpine\n");
  write("svc/pyproject.toml", "[project]\n");
  write("web/package.json", "{}");
  write("web/node_modules/left-pad/package.json", "{}");
  write("svc/vendor/dep/go.mod", "module dep\n");

  const found = discoverManifests(root);
  assert.deepEqual(
    found.map((m) => m.path),
    ["go.mod", "svc/Dockerfile", "svc/Dockerfile.debug", "svc/pyproject.toml", "web/package.json"],
  );
  assert.deepEqual(
    found.map((m) => m.ecosystem),
    ["gomod", "docker", "docker", "pip", "npm"],
  );
  assert.equal(found[0].directory, "");
});

test("requirements files are recognised as pip manifests", () => {
  const root = mkdtempSync(join(tmpdir(), "dependabot-discover-"));
  writeFileSync(join(root, "requirements.txt"), "");
  writeFileSync(join(root, "requirements-dev.txt"), "");
  assert.deepEqual(
    discoverManifests(root).map((m) => m.ecosystem),
    ["pip", "pip"],
  );
});

test("generated recipes must not receive Dependabot edits", () => {
  const recipe = manifest("docker", "module/services/store/builder", "Dockerfile");
  assert.deepEqual(run({ manifests: [recipe] }), []);
  const errors = run({ entries: [entry("docker", recipe.directory)], manifests: [recipe] });
  assert.equal(errors.length, 1);
  assert.match(errors[0], /agent-generated/);
});

test("versioned build recipe copies are also agent-generated", () => {
  const recipe = manifest("docker", "module/services/store/build-recipes/1.0.0/builder", "Dockerfile");
  assert.deepEqual(run({ manifests: [recipe] }), []);
  assert.match(run({ entries: [entry("docker", recipe.directory)], manifests: [recipe] })[0], /agent-generated/);
});
