#!/usr/bin/env node
// kit-version — the published frontend kit's version must move whenever its
// content does.
//
// A registry version is immutable: once `@codefly-dev/ui@0.1.0` is published,
// the bytes under that version are fixed forever. `publish-frontend-kit.mjs`
// already refuses to contradict that — it compares the freshly built tarball's
// integrity against the registry's before publishing — but it only learns of a
// stale version at release time, after the tag is cut, and that is where the
// v0.0.58 release stopped with the kit's content several releases ahead of the
// 0.1.0 the registry serves. Until such a release fails, every consuming
// solution that installs the kit from the registry resolves those first bytes
// while the host serves its newer workspace copy, and the two occupy one
// Module-Federation singleton slot keyed by version, so the drift is invisible
// at runtime.
//
// This is the pull-request half of the same invariant. The newest release tag
// is the baseline of what the registry serves, so a published package whose
// content moved since that tag must carry a version that differs from the
// tag's. Git history is the memory deliberately: reading the registry would
// mean handing a publish-capable token to every branch build.
//
//   node scripts/ci/kit-version.mjs check

import { execFileSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const REPOSITORY_ROOT = resolve(dirname(SCRIPT_PATH), "..", "..");

const KIT_ROOT = "module/services/frontend/code/packages";

// The packages `publish-frontend-kit.mjs` publishes, mapped to the directory
// whose content lands in each tarball. That script's `PACKAGES` stays the
// source of truth for *what* is published; kit-version.test.mjs asserts this
// list covers exactly it, so a package added there cannot escape the gate.
export const PUBLISHED_KIT_PACKAGES = [
  { name: "@codefly-dev/ui", directory: `${KIT_ROOT}/codefly-ui` },
  { name: "@codefly-dev/saas-sdk", directory: `${KIT_ROOT}/saas-sdk` },
];

// The deploy-counter tags, newest first, as git version-sorts them.
const RELEASE_TAG_PATTERN = "v[0-9]*";

const git = (...args) =>
  execFileSync("git", args, {
    cwd: REPOSITORY_ROOT,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });

const lines = (output) =>
  output
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);

// The newest release tag that does not point at HEAD. On a release run HEAD
// *is* the tag, and comparing a release against itself would gate nothing, so
// the predecessor is the baseline there.
export function newestBaseline({ tags, tagsAtHead }) {
  const atHead = new Set(tagsAtHead);
  return tags.find((tag) => !atHead.has(tag)) ?? null;
}

function baselineTag() {
  return newestBaseline({
    tags: lines(
      git(
        "tag",
        "--list",
        RELEASE_TAG_PATTERN,
        "--merged",
        "HEAD",
        "--sort=-v:refname",
      ),
    ),
    tagsAtHead: lines(git("tag", "--points-at", "HEAD")),
  });
}

// The package's declared version at `ref`, or null when it did not exist there.
function versionAt(ref, directory) {
  try {
    return JSON.parse(git("show", `${ref}:${directory}/package.json`)).version;
  } catch (error) {
    const stderr = String(error.stderr ?? "");
    if (/does not exist|exists on disk/.test(stderr)) return null;
    throw error;
  }
}

function changedSince(ref, directory) {
  try {
    git("diff", "--quiet", ref, "HEAD", "--", directory);
    return false;
  } catch (error) {
    if (error.status === 1) return true;
    throw error;
  }
}

// One message per package whose content moved without its version. A package
// absent from the baseline reads as a version change (null differs from any
// version): it has nothing published to contradict.
export function staleVersionErrors({ baseline, packages }) {
  return packages
    .filter(({ changed, version, baselineVersion }) => changed && version === baselineVersion)
    .map(
      ({ name, version }) =>
        `${name}: its content changed since ${baseline} but its version is still ${version}`,
    );
}

function check() {
  const baseline = baselineTag();
  if (!baseline) {
    console.error(
      "kit-version: no release tag is reachable from HEAD, so there is no published " +
        "baseline to compare against. This gate needs full history and tags " +
        "(actions/checkout with fetch-depth: 0).",
    );
    process.exit(2);
  }
  const packages = PUBLISHED_KIT_PACKAGES.map(({ name, directory }) => ({
    name,
    directory,
    changed: changedSince(baseline, directory),
    version: versionAt("HEAD", directory),
    baselineVersion: versionAt(baseline, directory),
  }));
  const errors = staleVersionErrors({ baseline, packages });
  if (errors.length) {
    console.error("kit-version: a published frontend kit package changed without a version bump:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} package(s) would publish changed bytes under a version the ` +
        "registry already serves, which it refuses — and until it does, consumers keep " +
        "resolving the older bytes. Bump the version in the package's package.json. The kit " +
        "is co-versioned: @codefly-dev/ui moves with @codefly/saas-ui and with " +
        "CODEFLY_KIT_VERSION in src/solutions/SolutionOutlet.tsx, which the " +
        "kit-shared-version test pins to both.",
    );
    process.exit(1);
  }
  const moved = packages.filter(({ changed }) => changed);
  if (moved.length === 0) {
    console.log(`✓ no published frontend kit package changed since ${baseline}.`);
    return;
  }
  console.log(
    `✓ every published frontend kit package that changed since ${baseline} carries a new ` +
      `version: ${moved.map(({ name, version }) => `${name}@${version}`).join(", ")}.`,
  );
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  if (process.argv[2] === "check") check();
  else {
    console.error("usage: kit-version.mjs check");
    process.exit(2);
  }
}
