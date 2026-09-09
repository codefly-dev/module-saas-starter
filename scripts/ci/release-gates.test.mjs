import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import {
  AFFECTED_SCOPED_GATES,
  AGGREGATE_JOB,
  REQUIRED_GATES,
  isReleaseTag,
  needsClosure,
  publicationReasons,
  publicationVerdictErrors,
  releaseGateContractErrors,
  releaseGateGraphErrors,
} from "./release-gates.mjs";
import { parseWorkflowYaml } from "./workflow-yaml.mjs";

const REPOSITORY_ROOT = join(import.meta.dirname, "..", "..");
const CI_WORKFLOW = join(REPOSITORY_ROOT, ".github", "workflows", "ci.yml");

const MODULE_TAG = "refs/tags/module-package/v1.2.3";
const DEPLOY_TAG = "refs/tags/v0.0.99";
const BRANCH = "refs/heads/main";

// `toJSON(needs)` for a run where everything the aggregate depends on passed.
function allSucceeded(overrides = {}) {
  const results = {};
  for (const gate of REQUIRED_GATES) results[gate] = { result: "success", outputs: {} };
  results["codefly-plan"].outputs = { has_work: "true" };
  return { ...results, ...overrides };
}

// ---------------------------------------------------------------------------
// decide — the aggregate job's runtime verdict
// ---------------------------------------------------------------------------

test("a complete successful run authorizes publication on both tag tracks", () => {
  for (const ref of [MODULE_TAG, DEPLOY_TAG, BRANCH]) {
    assert.deepEqual(publicationVerdictErrors({ results: allSucceeded(), ref }), []);
  }
});

test("a failed, cancelled, or skipped authz-coverage blocks publication", () => {
  for (const result of ["failure", "cancelled", "skipped"]) {
    for (const ref of [MODULE_TAG, DEPLOY_TAG, BRANCH]) {
      const results = allSucceeded({ "authz-coverage": { result, outputs: {} } });
      assert.deepEqual(publicationVerdictErrors({ results, ref }), [`authz-coverage: ${result}`]);
    }
  }
});

test("every mandatory gate blocks publication on its own", () => {
  for (const gate of REQUIRED_GATES) {
    for (const result of ["failure", "cancelled", "skipped"]) {
      const results = allSucceeded({ [gate]: { result, outputs: {} } });
      const errors = publicationVerdictErrors({ results, ref: MODULE_TAG });
      assert.deepEqual(errors, [`${gate}: ${result}`], `${gate} ${result} must block`);
    }
  }
});

test("a gate dropped from the aggregate's needs blocks publication", () => {
  const results = allSucceeded();
  delete results["authz-coverage"];
  assert.deepEqual(publicationVerdictErrors({ results, ref: MODULE_TAG }), [
    `authz-coverage: absent — it is not a dependency of ${AGGREGATE_JOB}`,
  ]);
});

test("a no-affected-service plan may skip the affected-scoped gates off a release tag", () => {
  const results = allSucceeded({ "codefly-plan": { result: "success", outputs: { has_work: "false" } } });
  for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }), []);
  assert.deepEqual(publicationVerdictErrors({ results, ref: "refs/pull/7/merge" }), []);
});

test("that exemption does not apply on a release tag", () => {
  const results = allSucceeded({ "codefly-plan": { result: "success", outputs: { has_work: "false" } } });
  for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
  for (const ref of [MODULE_TAG, DEPLOY_TAG]) {
    assert.deepEqual(
      publicationVerdictErrors({ results, ref }).sort(),
      AFFECTED_SCOPED_GATES.map((gate) => `${gate}: skipped`).sort(),
    );
  }
});

test("that exemption does not apply when the plan did find work", () => {
  const results = allSucceeded();
  results["codefly-build"] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }), ["codefly-build: skipped"]);
});

test("that exemption does not cover a gate outside the affected-scoped set", () => {
  const results = allSucceeded({ "codefly-plan": { result: "success", outputs: { has_work: "false" } } });
  results["marketing"] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }), ["marketing: skipped"]);
});

test("a skipped plan job cannot exempt the gates it scopes", () => {
  const results = allSucceeded({ "codefly-plan": { result: "skipped", outputs: {} } });
  for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }).sort(), [
    "codefly-build: skipped",
    "codefly-plan: skipped",
    "codefly-quality: skipped",
    "codefly-supply-chain: skipped",
  ]);
});

test("both tag tracks are recognized as release refs", () => {
  assert.equal(isReleaseTag(DEPLOY_TAG), true);
  assert.equal(isReleaseTag(MODULE_TAG), true);
  assert.equal(isReleaseTag(BRANCH), false);
  assert.equal(isReleaseTag("refs/tags/nightly"), false);
});

// ---------------------------------------------------------------------------
// check — the static workflow contract
// ---------------------------------------------------------------------------

const gateJob = (id) => `  ${id}:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${id}\n`;

// A minimal but complete workflow: every mandatory gate, the aggregate, and
// whatever extra job text a test wants to graft on.
function workflow({
  aggregateNeeds = REQUIRED_GATES,
  aggregateIf = "${{ always() }}",
  aggregateRun = "node scripts/ci/release-gates.mjs decide",
  gates = REQUIRED_GATES,
  extra = "",
} = {}) {
  return [
    "name: ci\n",
    "on:\n  push:\n    tags: [\"v*\"]\n",
    "permissions:\n  contents: read\n",
    "jobs:\n",
    gates.map(gateJob).join(""),
    `  ${AGGREGATE_JOB}:\n`,
    `    if: ${aggregateIf}\n`,
    "    needs:\n",
    aggregateNeeds.map((need) => `      - ${need}\n`).join(""),
    "    runs-on: ubuntu-latest\n",
    "    steps:\n",
    `      - run: ${aggregateRun}\n`,
    extra,
  ].join("");
}

const publisher = (id, { needs = [AGGREGATE_JOB], body } = {}) =>
  `  ${id}:\n` +
  (needs.length ? `    needs:\n${needs.map((n) => `      - ${n}\n`).join("")}` : "") +
  "    runs-on: ubuntu-latest\n    steps:\n" +
  body;

const RELEASE_STEP = '      - run: gh release create "${GITHUB_REF_NAME}"\n';

test("the shipped workflows satisfy the release-gate contract", () => {
  assert.deepEqual(releaseGateGraphErrors(REPOSITORY_ROOT), []);
});

test("every artifact-writing job in ci.yml transitively requires every mandatory gate", () => {
  const document = parseWorkflowYaml(readFileSync(CI_WORKFLOW, "utf8"));
  const publishers = Object.entries(document.jobs).filter(
    ([, job]) => publicationReasons(job, document.permissions).length > 0,
  );
  assert.ok(publishers.length >= 3, "ci.yml must still contain its publication jobs");
  for (const [name] of publishers) {
    const closure = needsClosure(name, document.jobs);
    assert.ok(closure.has(AGGREGATE_JOB), `${name} must depend on ${AGGREGATE_JOB}`);
    for (const gate of REQUIRED_GATES) {
      assert.ok(closure.has(gate), `${name} must transitively require ${gate}`);
    }
  }
});

test("a fully wired publication job passes", () => {
  const text = workflow({ extra: publisher("publish", { body: RELEASE_STEP }) });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), []);
});

test("an aggregate dependency reached transitively is enough", () => {
  const text = workflow({
    extra:
      "  stage:\n    needs:\n      - release-gates\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo stage\n" +
      publisher("publish", { needs: ["stage"], body: RELEASE_STEP }),
  });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), []);
});

test("a future publish job that misses the aggregate is rejected", () => {
  const text = workflow({
    extra: publisher("publish", { needs: ["base-integrity"], body: RELEASE_STEP }),
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /job publish writes artifacts .* neither directly nor transitively depends on release-gates/);
});

test("a publish job with no dependencies at all is rejected", () => {
  const text = workflow({ extra: publisher("publish", { needs: [], body: RELEASE_STEP }) });
  assert.equal(releaseGateContractErrors("w.yml", text).length, 1);
});

test("removing authz-coverage from the aggregate's needs is rejected", () => {
  const text = workflow({
    aggregateNeeds: REQUIRED_GATES.filter((gate) => gate !== "authz-coverage"),
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), [
    "w.yml: required gate authz-coverage is not a dependency of release-gates",
  ]);
});

test("deleting the authz-coverage job entirely is rejected", () => {
  const remaining = REQUIRED_GATES.filter((gate) => gate !== "authz-coverage");
  const text = workflow({
    gates: remaining,
    aggregateNeeds: remaining,
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), [
    "w.yml: required gate authz-coverage is not a job in this workflow",
  ]);
});

test("an aggregate that does not run with always() is rejected", () => {
  const text = workflow({
    aggregateIf: "${{ github.event_name == 'push' }}",
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /must run with always\(\)/);
});

test("an aggregate that never evaluates its dependencies' outcomes is rejected", () => {
  const text = workflow({
    aggregateRun: "echo all good",
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /must run `release-gates\.mjs decide`/);
});

test("an aggregate that itself writes artifacts is rejected", () => {
  const text = workflow({
    aggregateRun:
      'node scripts/ci/release-gates.mjs decide && gh release create "${GITHUB_REF_NAME}"',
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /release-gates must not write artifacts/);
});

test("a needs entry naming no job is rejected before anything else", () => {
  const text = workflow({
    extra: publisher("publish", { needs: ["release-gates", "ghost"], body: RELEASE_STEP }),
  });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), [
    "w.yml: job publish needs ghost, which is not a job here",
  ]);
});

test("an unparsable workflow fails the contract instead of passing empty", () => {
  const errors = releaseGateContractErrors("w.yml", "jobs:\n  a:\n    steps: {run: echo}\n");
  assert.equal(errors.length, 1);
  assert.match(errors[0], /could not be parsed/);
});

test("a needs cycle among publication jobs fails rather than looping", () => {
  const text = workflow({
    extra:
      publisher("publish", { needs: ["release-gates", "loop"], body: RELEASE_STEP }) +
      "  loop:\n    needs:\n      - publish\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo loop\n",
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /needs cycle/);
});

test("a workflow that publishes nothing needs no aggregate", () => {
  const text = [
    "name: dep-audit\n",
    "on:\n  schedule:\n    - cron: \"17 6 * * *\"\n",
    "permissions:\n  contents: write\n  pull-requests: write\n",
    "jobs:\n",
    "  remediate:\n    runs-on: ubuntu-latest\n    steps:\n",
    "      - run: |\n          git push --force origin chore/dep-audit-remediation\n          gh pr create --base main --title x --body y\n",
  ].join("");
  assert.deepEqual(releaseGateContractErrors("dep-audit.yml", text), []);
});

// ---------------------------------------------------------------------------
// artifact-writing detection
// ---------------------------------------------------------------------------

test("each artifact-writing signal marks a job as a publisher", () => {
  const signals = [
    { steps: [{ run: 'gh release upload "$TAG" out/*' }] },
    { steps: [{ run: "npm publish --access restricted" }] },
    { steps: [{ run: "node scripts/publish-frontend-kit.mjs" }] },
    { steps: [{ run: "docker push ghcr.io/example/app:1" }] },
    { steps: [{ run: 'gh api "repos/${OWNER}/${REPO}/dispatches" \\\n  -f event_type=surface-bump' }] },
    { steps: [{ uses: "actions/attest-build-provenance@v4" }] },
    { permissions: { packages: "write" }, steps: [] },
    { permissions: { "id-token": "write" }, steps: [] },
    { permissions: { attestations: "write" }, steps: [] },
  ];
  for (const job of signals) {
    assert.ok(publicationReasons(job, { contents: "read" }).length > 0, JSON.stringify(job));
  }
});

test("ordinary checks and a branch-pushing job are not publishers", () => {
  const jobs = [
    { steps: [{ run: "go test ./..." }] },
    { steps: [{ run: "gh release view v1.0.0" }] },
    { steps: [{ run: "gh pr create --base main" }] },
    { permissions: { contents: "write", "pull-requests": "write" }, steps: [{ run: "git push origin HEAD" }] },
    { steps: [{ uses: "anchore/sbom-action/download-syft@v0.24.0" }] },
  ];
  for (const job of jobs) assert.deepEqual(publicationReasons(job, { contents: "read" }), []);
});

test("a job inherits the workflow's publication permissions", () => {
  assert.ok(publicationReasons({ steps: [] }, { packages: "write" }).length > 0);
  assert.deepEqual(publicationReasons({ permissions: { contents: "read" }, steps: [] }, { packages: "write" }), []);
});

// ---------------------------------------------------------------------------
// the workflow reader
// ---------------------------------------------------------------------------

test("a block scalar keeps its body and hides it from the mapping parser", () => {
  const document = parseWorkflowYaml(
    [
      "jobs:",
      "  a:",
      "    steps:",
      "      - name: run it",
      "        run: |",
      "          # not a yaml comment",
      "          if [[ -n \"${X}\" ]]; then",
      "            echo 'needs: nobody'",
      "",
      "          fi",
      "      - run: echo done",
      "",
    ].join("\n"),
  );
  const [first, second] = document.jobs.a.steps;
  assert.equal(first.name, "run it");
  assert.equal(
    first.run,
    "# not a yaml comment\nif [[ -n \"${X}\" ]]; then\n  echo 'needs: nobody'\n\nfi\n",
  );
  assert.equal(second.run, "echo done");
});

test("comments are stripped outside quotes and kept inside them", () => {
  const document = parseWorkflowYaml(
    [
      "jobs:",
      "  # a leading comment",
      "  a:",
      "    uses: actions/checkout@abc # v7.0.0",
      '    run: echo "sharp # sign"',
      "",
    ].join("\n"),
  );
  assert.equal(document.jobs.a.uses, "actions/checkout@abc");
  assert.equal(document.jobs.a.run, 'echo "sharp # sign"');
});

test("flow and block sequences both read as arrays", () => {
  const document = parseWorkflowYaml(
    ["on:", '  push:', '    tags: ["v*", "module-package/v*"]', "jobs:", "  a:", "    needs:", "      - x", "      - y", ""].join("\n"),
  );
  assert.deepEqual(document.on.push.tags, ["v*", "module-package/v*"]);
  assert.deepEqual(document.jobs.a.needs, ["x", "y"]);
});

test("unsupported YAML throws instead of parsing to a guess", () => {
  assert.throws(() => parseWorkflowYaml("a: {b: c}\n"), /flow mappings are not supported/);
  assert.throws(() => parseWorkflowYaml("a: &anchor\n"), /anchors and aliases are not supported/);
  assert.throws(() => parseWorkflowYaml("a:\n  b: 1\n   c: 2\n"), /unexpected indentation/);
  assert.throws(() => parseWorkflowYaml("a: [1, [2]]\n"), /nested flow collections/);
});
