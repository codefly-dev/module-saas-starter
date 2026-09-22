import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { parseWorkflowYaml } from "./workflow-yaml.mjs";
import { releaseRecord, selectedAgents, selectedCodeflyVersion, serviceManifests } from "./release-record.mjs";

const REPOSITORY_ROOT = join(import.meta.dirname, "..", "..");
const read = (path) => readFileSync(join(REPOSITORY_ROOT, path), "utf8");

const MANIFESTS = serviceManifests();
const INSTALLER = read("scripts/ci/install-codefly.sh");

const manifestsOf = (...services) =>
  Object.fromEntries(services.map(({ name, agent, version }) =>
    [name, [`name: ${name}`, "agent:", `  name: ${agent}`, ...(version ? [`  version: ${version}`] : [])].join("\n")]));

// One agent backs several services — `go-grpc` three of them — and it is the
// agent version a consumer matches against, so the record collapses them to a
// row rather than repeating the pin once per service.
test("an agent shared by several services is named once, with each of them", () => {
  const agents = selectedAgents(manifestsOf(
    { name: "accounts", agent: "go-grpc", version: "0.1.39" },
    { name: "cache", agent: "redis", version: "0.0.89" },
    { name: "telemetry", agent: "go-grpc", version: "0.1.39" },
  ));
  assert.deepEqual(agents, [
    { name: "go-grpc", version: "0.1.39", services: ["accounts", "telemetry"] },
    { name: "redis", version: "0.0.89", services: ["cache"] },
  ]);
});

// Two services on the same agent at different versions are two distinct pins;
// folding them together would name a version half the graph does not run.
test("one agent at two versions stays two rows", () => {
  assert.deepEqual(
    selectedAgents(manifestsOf(
      { name: "frontend", agent: "nextjs", version: "0.0.153" },
      { name: "marketing", agent: "nextjs", version: "0.0.150" },
    )).map((agent) => `${agent.name}@${agent.version}`),
    ["nextjs@0.0.150", "nextjs@0.0.153"],
  );
});

// The record is published, and a consumer reads it to reproduce the package. A
// missing pin has to stop the release, never round down to a shorter table that
// silently omits the service whose version could not be read.
test("an unreadable pin fails the record rather than shortening it", () => {
  assert.throws(
    () => selectedAgents(manifestsOf({ name: "cache", agent: "redis" })),
    /module\/services\/cache\/service\.codefly\.yaml: pins no agent version/,
  );
  assert.throws(() => selectedAgents(manifestsOf()), /module\.codefly\.yaml: no services to describe/);
  assert.throws(() => selectedCodeflyVersion("version=\"0.1.155\"\n"), /no default CLI version/);
});

test("the CLI version is the installer's default, not a copy kept here", () => {
  assert.equal(selectedCodeflyVersion('version="${CODEFLY_VERSION:-9.9.9}"\n'), "9.9.9");
  assert.match(selectedCodeflyVersion(INSTALLER), /^\d+\.\d+\.\d+$/);
});

// The whole point of reading both sources is that the record cannot drift from
// them: every agent this repository actually pins, and the CLI CI actually
// installs, must appear in the published notes.
test("the record names every version this repository selects", () => {
  const record = releaseRecord(MANIFESTS, INSTALLER);
  for (const [name, text] of Object.entries(MANIFESTS)) {
    const { agent } = parseWorkflowYaml(text);
    assert.match(record, new RegExp(`\`${agent.name}\` \\| \`${agent.version.replaceAll(".", "\\.")}\``));
    assert.ok(record.includes(name), `${name} is absent from the release record`);
  }
  assert.match(record, new RegExp(`Codefly CLI \`${selectedCodeflyVersion(INSTALLER).replaceAll(".", "\\.")}\``));
  assert.match(record, /^## Selected versions$/m);
});
