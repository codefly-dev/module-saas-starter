// dependabot-coverage — the contract between .github/dependabot.yml and the tree.
//
// Two failure modes motivate this gate, and neither is visible by reading the
// config alone.
//
// A configured manifest that is hashed in module/tools/base-manifest.json
// produces a pull request that can NEVER merge. Dependabot bumps the manifest,
// the hash in base-manifest.json goes stale, and `base-integrity` — a gate in
// REQUIRED_GATES — fails. Dependabot cannot run `base-integrity.mjs gen`, and a
// workflow that regenerates the manifest onto Dependabot's own branch does not
// rescue it: a commit pushed with GITHUB_TOKEN starts no new workflow run, so
// the required checks never report against the new head. The unmergeable pull
// request then consumes that ecosystem's open-pull-requests-limit and blocks
// every later update behind it. The symptom is one stale red bot pull request,
// which reads exactly like ordinary bot noise.
//
// The mirror failure is silence: a manifest that is neither base-tracked nor
// configured receives no updates at all and reports nothing at all.
//
// So: every non-base-tracked dependency manifest must be configured, every
// base-tracked one must not be, and no entry may point at a directory holding
// no manifest of its ecosystem.

import { readdirSync, readFileSync, existsSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { parseWorkflowYaml } from "./workflow-yaml.mjs";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const REPOSITORY_ROOT = resolve(dirname(SCRIPT_PATH), "..", "..");

export const CONFIG_PATH = ".github/dependabot.yml";
export const BASE_MANIFEST_PATH = "module/tools/base-manifest.json";

// Which filenames declare dependencies for which Dependabot ecosystem.
export const MANIFEST_MATCHERS = [
  ["gomod", (name) => name === "go.mod"],
  ["npm", (name) => name === "package.json"],
  [
    "pip",
    (name) =>
      name === "pyproject.toml" ||
      name === "Pipfile" ||
      name === "setup.py" ||
      /^requirements[\w.-]*\.txt$/.test(name),
  ],
  ["docker", (name) => name === "Dockerfile" || name.startsWith("Dockerfile.")],
];

// Directories holding installed or emitted copies of other people's manifests.
// Walking into them would demand an entry per transitive dependency on disk.
const NOT_SOURCE = new Set([
  ".git",
  "node_modules",
  "vendor",
  "dist",
  "build",
  "out",
  ".next",
  ".venv",
  "__pycache__",
  "coverage",
]);

// The github-actions ecosystem has no manifest file of its own: Dependabot reads
// .github/workflows, and the directory is required to be `/`.
export const ACTIONS_DIRECTORY = "";

// A Dependabot `directory:` (`/`, `/module/tools`) as a repository-relative
// path, with the root spelled "".
export function normalizeDirectory(directory) {
  return String(directory).replace(/^\/+/, "").replace(/\/+$/, "");
}

// Every update entry as { ecosystem, directories }, with the singular
// `directory:` and plural `directories:` forms flattened into one shape.
export function dependabotEntries(text) {
  const document = parseWorkflowYaml(text);
  const updates = document?.updates ?? [];
  if (!Array.isArray(updates)) throw new Error("`updates` must be a list");
  return updates.map((update) => {
    const listed = update?.directories ?? (update?.directory === undefined ? [] : [update.directory]);
    return {
      ecosystem: update?.["package-ecosystem"],
      directories: (Array.isArray(listed) ? listed : [listed]).map(normalizeDirectory),
    };
  });
}

// The base-file paths as repository-relative paths. base-manifest.json keys are
// relative to module/, the directory the manifest describes.
export function baseTrackedPaths(root = REPOSITORY_ROOT) {
  const path = join(root, BASE_MANIFEST_PATH);
  if (!existsSync(path)) throw new Error(`${BASE_MANIFEST_PATH} is missing under ${root}`);
  const { files } = JSON.parse(readFileSync(path, "utf8"));
  return new Set(Object.keys(files ?? {}).map((relative) => `module/${relative}`));
}

// Every dependency manifest in the tree as { ecosystem, directory, path }.
export function discoverManifests(root = REPOSITORY_ROOT) {
  const found = [];
  const walk = (absolute, relative) => {
    for (const entry of readdirSync(absolute, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        if (NOT_SOURCE.has(entry.name)) continue;
        walk(join(absolute, entry.name), relative ? `${relative}/${entry.name}` : entry.name);
        continue;
      }
      if (!entry.isFile()) continue;
      for (const [ecosystem, matches] of MANIFEST_MATCHERS) {
        if (!matches(entry.name)) continue;
        found.push({
          ecosystem,
          directory: relative,
          path: relative ? `${relative}/${entry.name}` : entry.name,
        });
      }
    }
  };
  walk(root, "");
  return found.sort((a, b) => a.path.localeCompare(b.path));
}

const key = (ecosystem, directory) => `${ecosystem} ${directory}`;
const spell = (directory) => (directory === "" ? "/" : `/${directory}`);

export function coverageErrors({ entries, manifests, baseTracked, hasWorkflows }) {
  const errors = [];
  const configured = new Set();
  for (const { ecosystem, directories } of entries) {
    for (const directory of directories) configured.add(key(ecosystem, directory));
  }

  const byDirectory = new Map();
  for (const manifest of manifests) {
    const at = key(manifest.ecosystem, manifest.directory);
    if (!byDirectory.has(at)) byDirectory.set(at, []);
    byDirectory.get(at).push(manifest);
  }

  for (const { ecosystem, directories } of entries) {
    for (const directory of directories) {
      if (ecosystem === "github-actions") {
        if (directory !== ACTIONS_DIRECTORY) {
          errors.push(
            `${CONFIG_PATH}: github-actions is configured at ${spell(directory)}; Dependabot reads ` +
              ".github/workflows and requires the directory to be /",
          );
        } else if (!hasWorkflows) {
          errors.push(
            `${CONFIG_PATH}: github-actions is configured but .github/workflows holds no workflow`,
          );
        }
        continue;
      }
      const here = byDirectory.get(key(ecosystem, directory)) ?? [];
      if (here.length === 0) {
        errors.push(
          `${CONFIG_PATH}: ${ecosystem} is configured at ${spell(directory)}, which holds no ` +
            `${ecosystem} manifest; the entry can never open a pull request`,
        );
        continue;
      }
      for (const manifest of here) {
        if (!baseTracked.has(manifest.path)) continue;
        errors.push(
          `${CONFIG_PATH}: ${ecosystem} is configured at ${spell(directory)}, whose ${manifest.path} ` +
            `is hashed in ${BASE_MANIFEST_PATH}; bumping it leaves that manifest stale and fails the ` +
            "required base-integrity gate, which Dependabot cannot repair, so the pull request can " +
            "never merge and blocks every later update in this ecosystem behind it",
        );
      }
    }
  }

  for (const manifest of manifests) {
    if (baseTracked.has(manifest.path)) continue;
    if (configured.has(key(manifest.ecosystem, manifest.directory))) continue;
    errors.push(
      `${CONFIG_PATH}: ${manifest.path} is not base-tracked and no ${manifest.ecosystem} entry ` +
        `covers ${spell(manifest.directory)}; it would receive no updates and report nothing`,
    );
  }

  if (hasWorkflows && !configured.has(key("github-actions", ACTIONS_DIRECTORY))) {
    errors.push(
      `${CONFIG_PATH}: .github/workflows holds workflows but no github-actions entry covers /; the ` +
        "commit-digest pins nothing else moves would never be updated",
    );
  }

  return errors;
}

export function dependabotCoverageErrors(root = REPOSITORY_ROOT) {
  const configPath = join(root, CONFIG_PATH);
  if (!existsSync(configPath)) return [`${CONFIG_PATH} is missing under ${root}`];
  let entries;
  try {
    entries = dependabotEntries(readFileSync(configPath, "utf8"));
  } catch (error) {
    return [`${CONFIG_PATH}: could not be parsed: ${error.message}`];
  }
  const workflows = join(root, ".github", "workflows");
  const hasWorkflows =
    existsSync(workflows) && readdirSync(workflows).some((file) => /\.ya?ml$/.test(file));
  return coverageErrors({
    entries,
    manifests: discoverManifests(root),
    baseTracked: baseTrackedPaths(root),
    hasWorkflows,
  });
}

function check() {
  const errors = dependabotCoverageErrors();
  if (errors.length) {
    console.error("dependabot-coverage: the configuration does not match the tree:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} coverage defect(s). Every non-base-tracked dependency manifest must ` +
        "be configured, every base-tracked one must not be, and no entry may point at a directory " +
        "holding no manifest of its ecosystem.",
    );
    process.exit(1);
  }
  console.log(
    "dependabot-coverage: every updatable manifest is configured and no base file is.",
  );
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  if (process.argv[2] === "check") check();
  else {
    console.error("usage: dependabot-coverage.mjs check");
    process.exit(2);
  }
}
