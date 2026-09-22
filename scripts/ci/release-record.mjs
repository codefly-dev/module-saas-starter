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

// The source of truth for the service graph and the agent each service pins.
const BINDINGS_PATH = "module/deployment/topology.bindings.codefly.yaml";
// The CI installer's default is the one CLI every Codefly job runs.
const INSTALLER_PATH = "scripts/ci/install-codefly.sh";

// One row per distinct agent: `go-grpc` backs three services and `nextjs` two,
// and it is the agent version a consumer matches against, not the repetition.
export function selectedAgents(bindingsText) {
  const services = parseWorkflowYaml(bindingsText).services ?? [];
  const agents = new Map();
  for (const service of services) {
    const { name, version } = service.agent ?? {};
    if (!name || !version) {
      throw new Error(`${BINDINGS_PATH}: service ${service.name} pins no agent version`);
    }
    const key = `${name}@${version}`;
    if (!agents.has(key)) agents.set(key, { name, version, services: [] });
    agents.get(key).services.push(service.name);
  }
  if (agents.size === 0) throw new Error(`${BINDINGS_PATH}: no services to describe`);
  return [...agents.values()].sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
}

export function selectedCodeflyVersion(installerText) {
  const pin = /^version="\$\{CODEFLY_VERSION:-([^}"]+)\}"$/m.exec(installerText);
  if (!pin) throw new Error(`${INSTALLER_PATH}: no default CLI version to report`);
  return pin[1];
}

export function releaseRecord(bindingsText, installerText) {
  const rows = selectedAgents(bindingsText)
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
  process.stdout.write(releaseRecord(read(BINDINGS_PATH), read(INSTALLER_PATH)));
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
