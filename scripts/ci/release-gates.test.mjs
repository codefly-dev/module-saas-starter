import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  actionCommentErrors,
  actionPinErrors,
  AFFECTED_SCOPED_GATES,
  AGGREGATE_JOB,
  exemptedGates,
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
  results["codefly-plan"].outputs = { has_work: "true", all: "true" };
  return { ...results, ...overrides };
}

// The plan job as it reports a run with no affected service: an explicit
// has_work=false, and (off a release tag) a delta-scoped selection.
const scopedOutPlan = { result: "success", outputs: { has_work: "false", all: "false" } };

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
  const results = allSucceeded({ "codefly-plan": scopedOutPlan });
  for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }), []);
  assert.deepEqual(publicationVerdictErrors({ results, ref: "refs/pull/7/merge" }), []);
});

test("that exemption does not apply on a release tag", () => {
  const results = allSucceeded({ "codefly-plan": { ...scopedOutPlan, outputs: { has_work: "false", all: "true" } } });
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
  const results = allSucceeded({ "codefly-plan": scopedOutPlan });
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

test("an absent or unrecognized has_work exempts nothing", () => {
  // A renamed output or a lost GITHUB_OUTPUT write leaves has_work undefined.
  // The three affected-scoped jobs then skip on every branch push, and the
  // exemption must not quietly excuse them.
  for (const outputs of [{}, { has_work: "" }, { has_work: "unknown" }]) {
    const results = allSucceeded({ "codefly-plan": { result: "success", outputs } });
    for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
    assert.deepEqual(exemptedGates({ results, ref: BRANCH }), []);
    assert.deepEqual(
      publicationVerdictErrors({ results, ref: BRANCH }).sort(),
      AFFECTED_SCOPED_GATES.map((gate) => `${gate}: skipped`).sort(),
      `has_work=${JSON.stringify(outputs.has_work)} must not exempt anything`,
    );
  }
});

test("a scoped plan on a release ref blocks publication even with every gate green", () => {
  // Force-moving an existing tag carries a non-zero `before`, which used to
  // scope the mandatory gates to a delta: they pass without rebuilding or
  // re-auditing the services outside it.
  for (const ref of [MODULE_TAG, DEPLOY_TAG]) {
    const results = allSucceeded({
      "codefly-plan": { result: "success", outputs: { has_work: "true", all: "false" } },
    });
    assert.deepEqual(publicationVerdictErrors({ results, ref }), [
      "codefly-plan: resolved a scoped plan (all=false) on a release ref; the mandatory gates must verify the full topology",
    ]);
  }
});

test("a release ref with no plan scope reported at all blocks publication", () => {
  const results = allSucceeded({
    "codefly-plan": { result: "success", outputs: { has_work: "true" } },
  });
  assert.deepEqual(publicationVerdictErrors({ results, ref: DEPLOY_TAG }), [
    "codefly-plan: resolved a scoped plan (all=absent) on a release ref; the mandatory gates must verify the full topology",
  ]);
});

test("a delta-scoped plan is normal off a release ref", () => {
  const results = allSucceeded({ "codefly-plan": scopedOutPlan });
  for (const gate of AFFECTED_SCOPED_GATES) results[gate] = { result: "skipped", outputs: {} };
  assert.deepEqual(publicationVerdictErrors({ results, ref: BRANCH }), []);
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

test("an aggregate guarded by !cancelled() is accepted", () => {
  const text = workflow({
    aggregateIf: "${{ !cancelled() }}",
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  assert.deepEqual(releaseGateContractErrors("w.yml", text), []);
});

test("an aggregate that would skip past a failed gate is rejected", () => {
  const text = workflow({
    aggregateIf: "${{ github.event_name == 'push' }}",
    extra: publisher("publish", { body: RELEASE_STEP }),
  });
  const errors = releaseGateContractErrors("w.yml", text);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /must run with always\(\) or !cancelled\(\)/);
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
    { steps: [{ run: "node scripts/ci/announce-release.mjs" }] },
    { steps: [{ uses: "actions/attest-build-provenance@v4" }] },
    { permissions: { packages: "write" }, steps: [] },
    { permissions: { "id-token": "write" }, steps: [] },
    { permissions: { attestations: "write" }, steps: [] },
    // GitHub's scalar permissions form grants every scope above at once.
    { permissions: "write-all", steps: [] },
    // Actions that publish authenticate with a secret in `with:`, so they trip
    // neither the permission check nor the `run:` patterns.
    { steps: [{ uses: "example-org/repository-dispatch@v3" }] },
    { steps: [{ uses: "example-org/npm-publish@v3" }] },
    { steps: [{ uses: "example-org/action-gh-release@v2" }] },
    { steps: [{ uses: "example-org/create-release@v1" }] },
    { steps: [{ uses: "example-org/upload-release-asset@v1" }] },
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
    { permissions: "read-all", steps: [{ run: "npm test" }] },
  ];
  for (const job of jobs) assert.deepEqual(publicationReasons(job, { contents: "read" }), []);
});

test("no action already pinned in this repository is misread as a publisher", () => {
  // Guards the `uses:` patterns above against over-matching the real toolchain.
  const document = parseWorkflowYaml(readFileSync(CI_WORKFLOW, "utf8"));
  const uses = Object.values(document.jobs)
    .flatMap((job) => job.steps ?? [])
    .map((step) => step.uses)
    .filter(Boolean);
  const nonPublishing = uses.filter((ref) => !ref.startsWith("actions/attest-build-provenance"));
  assert.ok(nonPublishing.length > 5, "expected the pinned toolchain actions to be present");
  for (const ref of new Set(nonPublishing)) {
    assert.deepEqual(
      publicationReasons({ steps: [{ uses: ref }] }, { contents: "read" }),
      [],
      `${ref} must not be classified as a publisher`,
    );
  }
});

test("a job inherits the workflow's publication permissions", () => {
  assert.ok(publicationReasons({ steps: [] }, { packages: "write" }).length > 0);
  assert.deepEqual(publicationReasons({ permissions: { contents: "read" }, steps: [] }, { packages: "write" }), []);
});

// ---------------------------------------------------------------------------
// check — the action-pinning contract
// ---------------------------------------------------------------------------

const NODE_DIGEST = "49933ea5288caeca8642d1e84afbd3f7d6820020";

// One job, one step, whatever `uses:` a test wants to put in it.
const usingWorkflow = (ref) =>
  `jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ${ref}\n`;

test("every action the shipped workflows call is pinned to a digest", () => {
  const workflows = join(REPOSITORY_ROOT, ".github", "workflows");
  for (const file of readdirSync(workflows).filter((f) => /\.ya?ml$/.test(f))) {
    const text = readFileSync(join(workflows, file), "utf8");
    assert.deepEqual(actionPinErrors(`.github/workflows/${file}`, text), []);
  }
});

test("a digest ref passes, with or without a version comment or a subpath", () => {
  for (const ref of [
    `actions/setup-node@${NODE_DIGEST}`,
    `actions/setup-node@${NODE_DIGEST} # v4`,
    `anchore/sbom-action/download-syft@${NODE_DIGEST} # v0.24.0`,
  ]) {
    assert.deepEqual(actionPinErrors("w.yml", usingWorkflow(ref)), [], ref);
  }
});

test("an action in this repository needs no digest", () => {
  assert.deepEqual(actionPinErrors("w.yml", usingWorkflow("./.github/actions/setup")), []);
});

test("every way of naming a movable ref is rejected", () => {
  for (const ref of [
    "actions/setup-node@v4",
    "actions/setup-go@v6.5.0",
    "actions/checkout@main",
    "actions/checkout@49933ea",
    "actions/checkout",
    "docker://alpine:3.19",
    `docker://alpine@sha256:${"a".repeat(63)}`,
  ]) {
    const errors = actionPinErrors("w.yml", usingWorkflow(ref));
    assert.equal(errors.length, 1, ref);
    assert.match(errors[0], /job a uses .*, whose ref is mutable/);
  }
});

test("a container action pins to an image digest, and is told so when it does not", () => {
  // A `docker://` step has no commit digest to pin to, so demanding one would
  // be a false positive nothing could act on.
  const pinned = `docker://alpine@sha256:${"a".repeat(64)}`;
  assert.deepEqual(actionPinErrors("w.yml", usingWorkflow(pinned)), []);
  assert.match(
    actionPinErrors("w.yml", usingWorkflow("docker://alpine:3.19"))[0],
    /pin it to an image digest/,
  );
});

test("an uppercase commit digest names one commit and is accepted", () => {
  // Immutability is the property under test; letter case is not.
  assert.deepEqual(
    actionPinErrors("w.yml", usingWorkflow(`actions/checkout@${NODE_DIGEST.toUpperCase()}`)),
    [],
  );
});

test("a reusable-workflow call on the job itself must be pinned too", () => {
  const text = "jobs:\n  a:\n    uses: owner/repo/.github/workflows/ci.yml@v1\n";
  assert.equal(actionPinErrors("w.yml", text).length, 1);
  assert.deepEqual(
    actionPinErrors("w.yml", `jobs:\n  a:\n    uses: owner/repo/.github/workflows/ci.yml@${NODE_DIGEST}\n`),
    [],
  );
});

test("an unparsable workflow fails the pin check instead of passing empty", () => {
  const errors = actionPinErrors("w.yml", "jobs: {a: b}\n");
  assert.equal(errors.length, 1);
  assert.match(errors[0], /could not be parsed/);
});

// The `github-actions` entry in .github/dependabot.yml rewrites these digests on
// a schedule. The trailing comment is the only record of which release a digest
// is, and until now the pin remedy asked for it without ever checking it.
test("every action the shipped workflows call names its version in a comment", () => {
  const workflows = join(REPOSITORY_ROOT, ".github", "workflows");
  for (const file of readdirSync(workflows).filter((f) => /\.ya?ml$/.test(f))) {
    const text = readFileSync(join(workflows, file), "utf8");
    assert.deepEqual(actionCommentErrors(`.github/workflows/${file}`, text), []);
  }
});

test("a digest with no trailing version comment is rejected", () => {
  const errors = actionCommentErrors("w.yml", usingWorkflow(`actions/setup-node@${NODE_DIGEST}`));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /w\.yml:5: uses actions\/setup-node@/);
  assert.match(errors[0], /no trailing version comment/);
});

test("any non-empty trailing comment satisfies the rule", () => {
  for (const ref of [
    `actions/setup-node@${NODE_DIGEST} # v4`,
    `actions/setup-node@${NODE_DIGEST} # v4.1.0`,
    `actions/setup-node@${NODE_DIGEST}   #v4`,
  ]) {
    assert.deepEqual(actionCommentErrors("w.yml", usingWorkflow(ref)), [], ref);
  }
  // An empty comment records nothing, so it does not count as one.
  assert.equal(
    actionCommentErrors("w.yml", usingWorkflow(`actions/setup-node@${NODE_DIGEST} #`)).length,
    1,
  );
});

test("an action in this repository needs no version comment", () => {
  assert.deepEqual(actionCommentErrors("w.yml", usingWorkflow("./.github/actions/setup")), []);
});

// A mutable ref is the pin rule's defect, and its remedy already asks for the
// comment. Reporting it here too would describe one fix as two.
test("a mutable ref is left to the pin rule rather than reported twice", () => {
  for (const ref of ["actions/setup-node@v4", "docker://alpine:3.19"]) {
    assert.deepEqual(actionCommentErrors("w.yml", usingWorkflow(ref)), [], ref);
    assert.equal(actionPinErrors("w.yml", usingWorkflow(ref)).length, 1, ref);
  }
});

test("a pinned container action still names its version", () => {
  const pinned = `docker://alpine@sha256:${"a".repeat(64)}`;
  assert.equal(actionCommentErrors("w.yml", usingWorkflow(pinned)).length, 1);
  assert.deepEqual(actionCommentErrors("w.yml", usingWorkflow(`${pinned} # 3.19`)), []);
});

test("a reusable-workflow call on the job itself needs its version comment too", () => {
  const bare = `jobs:\n  a:\n    uses: owner/repo/.github/workflows/ci.yml@${NODE_DIGEST}\n`;
  assert.equal(actionCommentErrors("w.yml", bare).length, 1);
  assert.deepEqual(actionCommentErrors("w.yml", `${bare.trimEnd()} # v1\n`), []);
});

// A `run:` body is a block scalar, not steps. Scanning raw lines would read a
// `uses:` written inside one as a step and demand a comment for it.
test("a uses: line inside a run script is not mistaken for a step", () => {
  const text = [
    "jobs:",
    "  a:",
    "    runs-on: ubuntu-latest",
    "    steps:",
    `      - uses: actions/checkout@${NODE_DIGEST} # v7.0.0`,
    "      - run: |",
    "          echo 'uses: actions/setup-node@v4'",
    "          uses: not-a-step/at-all@deadbeef",
    "",
  ].join("\n");
  assert.deepEqual(actionCommentErrors("w.yml", text), []);
});

test("an unparsable workflow fails the comment check instead of passing empty", () => {
  const errors = actionCommentErrors("w.yml", "jobs: {a: b}\n");
  assert.equal(errors.length, 1);
  assert.match(errors[0], /could not be parsed/);
});

test("an unreadable workflow is one defect, not one per contract that reads it", () => {
  const root = mkdtempSync(join(tmpdir(), "release-gates-"));
  mkdirSync(join(root, ".github", "workflows"), { recursive: true });
  writeFileSync(join(root, ".github", "workflows", "broken.yml"), "jobs: {a: b}\n");
  const errors = releaseGateGraphErrors(root);
  assert.deepEqual(errors, [
    "broken.yml: could not be parsed: line 1: flow mappings are not supported"
      .replace("broken.yml", ".github/workflows/broken.yml"),
  ]);
});

test("the pin check reaches a workflow the publication contract exits early on", () => {
  // `releaseGateContractErrors` returns as soon as it finds no publisher, which
  // is every gate-only workflow. A mutable ref there must still fail `check`.
  const root = mkdtempSync(join(tmpdir(), "release-gates-"));
  mkdirSync(join(root, ".github", "workflows"), { recursive: true });
  const text = usingWorkflow("actions/setup-node@v4");
  writeFileSync(join(root, ".github", "workflows", "audit.yml"), text);
  assert.deepEqual(releaseGateContractErrors("audit.yml", text), []);
  const errors = releaseGateGraphErrors(root);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /audit\.yml: job a uses actions\/setup-node@v4/);
});

// An independent oracle over the raw text, in the same spirit as the job-set
// scan below: the gate can only pin what the reader hands it, so a `uses:` the
// parser drops is an unpinned ref nothing would ever flag.
//
// A `run: |` body is data, not YAML — a step that writes a workflow file holds
// `uses:` lines that are no more actions than a `needs:` inside one is a
// dependency. The scan tracks block scalars so it disagrees with the reader
// only when the reader has actually lost a ref.
function scanUsesRefs(text) {
  const refs = [];
  let blockIndent = null;
  for (const line of text.split("\n")) {
    if (blockIndent !== null) {
      if (/^\s*$/.test(line) || /^ */.exec(line)[0].length > blockIndent) continue;
      blockIndent = null;
    }
    const block = /^( *)(?:- )?[\w.-]+:\s*[|>][-+]?\s*$/.exec(line);
    if (block) {
      blockIndent = block[1].length;
      continue;
    }
    const match = /^ *(?:- )?uses:\s*(\S+)/.exec(line);
    if (match) refs.push(match[1]);
  }
  return refs;
}

test("the raw scan reads block-scalar bodies as data, not as steps", () => {
  const generating = [
    "jobs:",
    "  a:",
    "    steps:",
    "      - run: |",
    "          cat > gen.yml <<EOF",
    "          - uses: actions/setup-node@v4",
    "          EOF",
    `      - uses: actions/setup-node@${NODE_DIGEST}`,
    "",
  ].join("\n");
  assert.deepEqual(scanUsesRefs(generating), [`actions/setup-node@${NODE_DIGEST}`]);
  assert.deepEqual(actionPinErrors("w.yml", generating), []);
});

test("the reader and an independent text scan agree on every uses: ref", () => {
  const workflows = join(REPOSITORY_ROOT, ".github", "workflows");
  const files = readdirSync(workflows).filter((file) => /\.ya?ml$/.test(file));
  let total = 0;
  for (const file of files) {
    const text = readFileSync(join(workflows, file), "utf8");
    const scanned = scanUsesRefs(text);
    const document = parseWorkflowYaml(text);
    const read = Object.values(document.jobs).flatMap((job) => [
      ...(typeof job.uses === "string" ? [job.uses] : []),
      ...(job.steps ?? []).map((step) => step.uses).filter((ref) => typeof ref === "string"),
    ]);
    assert.deepEqual(
      read.sort(),
      scanned.sort(),
      `${file}: the reader and the raw scan disagree on the uses: refs`,
    );
    total += scanned.length;
  }
  assert.ok(total > 40, `expected the full toolchain, saw ${total} refs`);
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

// An independent oracle over the raw text. The contract is only as good as the
// reader beneath it, and the dangerous direction is under-detection: a job the
// parser silently drops is a publisher the graph check never sees. This scan
// shares no code with the parser, so the two must agree on the job set.
function scanJobIds(text) {
  const lines = text.split("\n");
  const start = lines.findIndex((line) => /^jobs:\s*$/.test(line));
  assert.ok(start >= 0, "the workflow must declare a top-level jobs: block");
  const ids = [];
  for (const line of lines.slice(start + 1)) {
    if (/^\S/.test(line)) break;
    const match = /^ {2}([A-Za-z_][\w-]*):\s*$/.exec(line);
    if (match) ids.push(match[1]);
  }
  return ids;
}

test("the reader and an independent text scan agree on every workflow's job set", () => {
  const workflows = join(REPOSITORY_ROOT, ".github", "workflows");
  const files = readdirSync(workflows).filter((file) => /\.ya?ml$/.test(file));
  assert.ok(files.length >= 2, "expected both shipped workflows");
  for (const file of files) {
    const text = readFileSync(join(workflows, file), "utf8");
    assert.deepEqual(
      Object.keys(parseWorkflowYaml(text).jobs).sort(),
      scanJobIds(text).sort(),
      `${file}: the reader and the raw scan disagree on the job set`,
    );
  }
});

test("every job in the shipped workflows reads back with usable steps", () => {
  const workflows = join(REPOSITORY_ROOT, ".github", "workflows");
  for (const file of readdirSync(workflows).filter((f) => /\.ya?ml$/.test(f))) {
    const document = parseWorkflowYaml(readFileSync(join(workflows, file), "utf8"));
    for (const [name, job] of Object.entries(document.jobs)) {
      assert.ok(Array.isArray(job.steps) && job.steps.length > 0, `${file}: ${name} lost its steps`);
      for (const step of job.steps) {
        assert.ok(
          typeof step.run === "string" || typeof step.uses === "string",
          `${file}: ${name} has a step with neither run nor uses`,
        );
      }
    }
  }
});

// ---------------------------------------------------------------------------
// the documented gate list vs. the enforced one
//
// RELEASE_GATES.md § Repository-specific gates and AGENTS.md both enumerate the
// gates that are not service gates, and both were hand-maintained. `kit-version`
// became mandatory in REQUIRED_GATES and neither list learned about it, so the
// document that exists to say what CI enforces disagreed with CI — and with the
// mandatory-gates table nine lines above it in the same file. Derive the
// repository-specific set from REQUIRED_GATES and hold the prose to it.

const RELEASE_GATES_DOC = join(REPOSITORY_ROOT, "RELEASE_GATES.md");
const AGENTS_DOC = join(REPOSITORY_ROOT, "AGENTS.md");
const CLAIM_INVENTORY_DOC = join(REPOSITORY_ROOT, "CLAIM_INVENTORY.md");

// Everything mandatory that `codefly ci run` does not own. The `codefly-` prefix
// is the service-gate namespace; anything else is this repository's own.
const REPOSITORY_SPECIFIC_GATES = REQUIRED_GATES.filter(
  (gate) => !gate.startsWith("codefly-"),
).sort();

const NUMBER_WORDS = {
  six: 6, seven: 7, eight: 8, nine: 9, ten: 10, eleven: 11, twelve: 12,
};

/** The job ids in the first column of the § Repository-specific gates table. */
function documentedRepositorySpecificGates(markdown) {
  const section = markdown.split("\n## Repository-specific gates\n")[1];
  assert.ok(section, "RELEASE_GATES.md has no § Repository-specific gates");
  const ids = [];
  for (const line of section.split("\n")) {
    if (line.startsWith("## ")) break;
    const row = /^\|\s*`([a-z0-9-]+)`\s*\|/.exec(line.trim());
    if (row) ids.push(row[1]);
  }
  return ids;
}

test("RELEASE_GATES.md documents exactly the mandatory repository-specific gates", () => {
  assert.deepEqual(
    documentedRepositorySpecificGates(readFileSync(RELEASE_GATES_DOC, "utf8")).sort(),
    REPOSITORY_SPECIFIC_GATES,
    "§ Repository-specific gates and REQUIRED_GATES disagree; document the gate or drop it",
  );
});

test("every documented repository-specific gate names what it runs", () => {
  const section = readFileSync(RELEASE_GATES_DOC, "utf8")
    .split("\n## Repository-specific gates\n")[1]
    .split("\n## ")[0];
  for (const gate of REPOSITORY_SPECIFIC_GATES) {
    const row = section.split("\n").find((line) => line.trim().startsWith(`| \`${gate}\``));
    const cells = row.split("|").map((cell) => cell.trim()).filter(Boolean);
    assert.equal(cells.length, 3, `${gate}: expected a job / guards / runs row`);
    assert.ok(cells[2].includes("`"), `${gate}: the "what it runs" cell names no command`);
  }
});

test("the prose counts of repository-specific gates match the enforced set", () => {
  const expected = REPOSITORY_SPECIFIC_GATES.length;
  const prose = [
    [AGENTS_DOC, /CI runs (\w+) repository-specific gates/],
    [CLAIM_INVENTORY_DOC, /(\w+) repository-specific jobs/],
  ];
  for (const [file, pattern] of prose) {
    const match = pattern.exec(readFileSync(file, "utf8"));
    assert.ok(match, `${file}: no repository-specific gate count to check`);
    assert.equal(
      NUMBER_WORDS[match[1]],
      expected,
      `${file}: says "${match[1]}" repository-specific gates; REQUIRED_GATES has ${expected}`,
    );
  }
});
