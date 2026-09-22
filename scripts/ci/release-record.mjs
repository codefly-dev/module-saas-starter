#!/usr/bin/env node
// release-record — the runtime versions the published package was built on.
//
// An immutable module package pins every service to an exact service-agent
// version and is produced by an exact CLI, but the release that carries it
// named neither: its notes held the trust policy and GitHub's generated commit
// list, so the first question a consumer asks of a package — which runtime
// produced these bytes — could only be answered by checking out the tag and
// reading the bindings. That is the one lookup a release record exists to
// spare, and it is the lookup a consumer cannot make at all once it is pinning
// by identity rather than by branch.
//
// Both facts are read from the sources CI itself obeys — the deployment
// bindings for the agents, the CI installer for the CLI — never restated here,
// so a record can never name a version the release did not use.
//
//   node scripts/ci/release-record.mjs

import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseWorkflowYaml } from "./workflow-yaml.mjs";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const REPOSITORY_ROOT = resolve(dirname(SCRIPT_PATH), "..", "..");

// The manifests are the model: module.codefly.yaml lists the services and each
// services/<name>/service.codefly.yaml names the agent that service runs.
const MODULE_MANIFEST_PATH = "module/module.codefly.yaml";
const serviceManifestPath = (name) => `module/services/${name}/service.codefly.yaml`;
// The CI installer's default is the one CLI every Codefly job runs.
const INSTALLER_PATH = "scripts/ci/install-codefly.sh";

// One row per distinct agent: `go-grpc` backs three services and `nextjs` two,
// and it is the agent version a consumer matches against, not the repetition.
// `manifests` maps a service name to its service.codefly.yaml text.
export function selectedAgents(manifests) {
  const agents = new Map();
  for (const [serviceName, text] of Object.entries(manifests)) {
    const { name, version } = parseWorkflowYaml(text).agent ?? {};
    if (!name || !version) {
      throw new Error(`${serviceManifestPath(serviceName)}: pins no agent version`);
    }
    const key = `${name}@${version}`;
    if (!agents.has(key)) agents.set(key, { name, version, services: [] });
    agents.get(key).services.push(serviceName);
  }
  if (agents.size === 0) throw new Error(`${MODULE_MANIFEST_PATH}: no services to describe`);
  return [...agents.values()].sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
}

// The service manifests the module declares, read from the tree.
export function serviceManifests(root = REPOSITORY_ROOT) {
  const module = parseWorkflowYaml(readFileSync(join(root, MODULE_MANIFEST_PATH), "utf8"));
  return Object.fromEntries((module.services ?? []).map(({ name }) =>
    [name, readFileSync(join(root, serviceManifestPath(name)), "utf8")]));
}

export function selectedCodeflyVersion(installerText) {
  const pin = /^version="\$\{CODEFLY_VERSION:-([^}"]+)\}"$/m.exec(installerText);
  if (!pin) throw new Error(`${INSTALLER_PATH}: no default CLI version to report`);
  return pin[1];
}

export function releaseRecord(manifests, installerText) {
  const rows = selectedAgents(manifests)
    .map((agent) => `| \`${agent.name}\` | \`${agent.version}\` | ${agent.services.join(", ")} |`)
    .join("\n");
  return [
    "## Selected versions",
    "",
    `Built and gated by the Codefly CLI \`${selectedCodeflyVersion(installerText)}\`.`,
    "",
    "| service agent | version | services |",
    "| --- | --- | --- |",
    rows,
    "",
  ].join("\n");
}

function main() {
  const read = (path) => readFileSync(join(REPOSITORY_ROOT, path), "utf8");
  process.stdout.write(releaseRecord(serviceManifests(), read(INSTALLER_PATH)));
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
