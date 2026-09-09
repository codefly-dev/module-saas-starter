#!/usr/bin/env node
// release-gates — release orchestration, in two halves.
//
// `authz-coverage` used to be an independent job that no publisher listed in
// `needs`, so a tag run could publish the immutable module package or the
// frontend kit while the authorization gate was red. A red overall workflow
// does not retract an artifact that is already public, and branch protection
// on pull requests does not create a tag-time dependency (audit finding A08).
//
// The fix is one aggregate job, `release-gates`, that every artifact-writing
// job depends on. It runs with `always()` so it can inspect its dependencies'
// outcomes itself: a bare `needs:` cannot distinguish a check that passed from
// one that never ran, and "skipped" must not read as consent to publish.
//
//   node scripts/ci/release-gates.mjs decide   # the aggregate job's verdict
//   node scripts/ci/release-gates.mjs check    # the static workflow contract
//
// `decide` is the runtime half: given `toJSON(needs)` it fails unless every
// mandatory gate actually succeeded. `check` is the static half: it parses the
// workflow dependency graph and fails unless every artifact-writing job — in
// any workflow, including one added later — is transitively dominated by the
// aggregate, and the aggregate by every mandatory gate.

import { readdirSync, readFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { parseWorkflowYaml } from "./workflow-yaml.mjs";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const REPOSITORY_ROOT = resolve(dirname(SCRIPT_PATH), "..", "..");

export const AGGREGATE_JOB = "release-gates";

// The job id of the static contract below, which is itself mandatory: without
// it the graph could be rewired and nothing would notice.
export const CONTRACT_JOB = "release-contract";

// Every check that must have actually succeeded before any artifact is
// written, on either tag track. There is no per-track exemption: the two
// tracks publish from the same tree, so a gate that is meaningful for one is
// meaningful for the other.
export const REQUIRED_GATES = [
  "authz-coverage",
  "base-integrity",
  "codefly-build",
  "codefly-plan",
  "codefly-quality",
  "codefly-supply-chain",
  "docs-sync",
  "marketing",
  "module-package",
  "provider-shim",
  CONTRACT_JOB,
  "sdk-boundary",
];

// The gates Codefly scopes to the affected-service plan. A branch or pull
// request whose plan has no affected service legitimately skips them; on a
// release tag their own `if:` forces them to run, so a skip there is a defect
// and never an exemption.
export const AFFECTED_SCOPED_GATES = [
  "codefly-build",
  "codefly-quality",
  "codefly-supply-chain",
];

const PLAN_JOB = "codefly-plan";

// Both tag tracks: the v0.0.x deploy counter and the immutable module package.
export const isReleaseTag = (ref) =>
  typeof ref === "string" &&
  (ref.startsWith("refs/tags/v") || ref.startsWith("refs/tags/module-package/v"));

// ---------------------------------------------------------------------------
// decide — the aggregate job's runtime verdict
// ---------------------------------------------------------------------------

// The gates this run may legitimately skip: none on a release tag, and none
// unless the plan job itself succeeded and reported no affected service.
export function exemptedGates({ results, ref }) {
  if (isReleaseTag(ref)) return [];
  const plan = results[PLAN_JOB];
  if (plan?.result !== "success" || plan?.outputs?.has_work === "true") return [];
  return AFFECTED_SCOPED_GATES;
}

// `results` is `toJSON(needs)`: job id -> { result, outputs }. Returns one
// message per gate that does not authorize publication.
export function publicationVerdictErrors({ results, ref }) {
  const exempt = exemptedGates({ results, ref });
  const errors = [];
  for (const gate of REQUIRED_GATES) {
    const result = results[gate]?.result;
    if (result === undefined) {
      errors.push(`${gate}: absent — it is not a dependency of ${AGGREGATE_JOB}`);
      continue;
    }
    if (result === "success") continue;
    if (result === "skipped" && exempt.includes(gate)) continue;
    errors.push(`${gate}: ${result}`);
  }
  return errors;
}

function decide() {
  const raw = process.env.GATE_RESULTS;
  if (!raw) {
    console.error("release-gates: GATE_RESULTS is required (set it to toJSON(needs)).");
    process.exit(2);
  }
  let results;
  try {
    results = JSON.parse(raw);
  } catch (error) {
    console.error(`release-gates: GATE_RESULTS is not valid JSON: ${error.message}`);
    process.exit(2);
  }
  const ref = process.env.GITHUB_REF ?? "";
  const errors = publicationVerdictErrors({ results, ref });
  if (errors.length) {
    console.error("release-gates: publication is blocked — a mandatory gate did not succeed:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} mandatory gate(s) did not succeed. No artifact-writing job ` +
        "may run for this reference.",
    );
    process.exit(1);
  }
  const skipped = exemptedGates({ results, ref }).filter(
    (gate) => results[gate]?.result === "skipped",
  );
  const scope = isReleaseTag(ref) ? ` for release ref ${ref}` : "";
  if (skipped.length) {
    console.log(
      `✓ ${REQUIRED_GATES.length - skipped.length} mandatory gates succeeded${scope}; ` +
        `${skipped.join(", ")} are scoped out by a plan with no affected service. ` +
        "Publication is authorized.",
    );
    return;
  }
  console.log(
    `✓ all ${REQUIRED_GATES.length} mandatory gates succeeded${scope}; publication is authorized.`,
  );
}

// ---------------------------------------------------------------------------
// check — the static workflow contract
// ---------------------------------------------------------------------------

// What makes a job an artifact writer. The step patterns match the `run` body
// or the `uses` reference of any step; the permissions are the three scopes
// that exist only to publish (a `contents: write` job may merely push a
// branch, as the dependency-audit remediation workflow does).
const PUBLICATION_STEP_PATTERNS = [
  { on: "run", pattern: /\bgh\s+release\s+(?:create|edit|upload|delete)\b/, why: "mutates a GitHub release" },
  { on: "run", pattern: /\bnpm\s+publish\b/, why: "publishes an npm package" },
  { on: "run", pattern: /\bpublish-[\w-]+\.mjs\b/, why: "runs a package publish script" },
  { on: "run", pattern: /\bdocker\s+push\b/, why: "pushes a container image" },
  { on: "run", pattern: /\bgh\s+api\b[\s\S]*?\/dispatches\b/, why: "dispatches a release event downstream" },
  { on: "uses", pattern: /^actions\/attest-build-provenance/, why: "attests artifact provenance" },
];
const PUBLICATION_PERMISSIONS = new Map([
  ["packages", "may write to the package registry"],
  ["id-token", "may mint an OIDC identity token"],
  ["attestations", "may write attestations"],
]);

const jobNeeds = (job) => {
  const needs = job?.needs;
  if (needs === undefined || needs === null) return [];
  return Array.isArray(needs) ? needs : [needs];
};

// Why this job writes artifacts, or an empty list if it does not.
export function publicationReasons(job, workflowPermissions) {
  const reasons = [];
  const permissions = job?.permissions ?? workflowPermissions;
  if (permissions && typeof permissions === "object") {
    for (const [scope, why] of PUBLICATION_PERMISSIONS) {
      if (permissions[scope] === "write") reasons.push(`it ${why} (permissions.${scope})`);
    }
  }
  for (const step of job?.steps ?? []) {
    for (const { on, pattern, why } of PUBLICATION_STEP_PATTERNS) {
      const subject = step?.[on];
      if (typeof subject === "string" && pattern.test(subject)) reasons.push(`a step ${why}`);
    }
  }
  return [...new Set(reasons)];
}

// Every job reachable from `start` through `needs`, `start` excluded. Throws on
// a cycle so an unresolvable graph fails the gate rather than looping.
export function needsClosure(start, jobs) {
  const seen = new Set();
  const walk = (name, path) => {
    for (const dependency of jobNeeds(jobs[name])) {
      if (path.includes(dependency)) {
        throw new Error(`needs cycle: ${[...path, dependency].join(" -> ")}`);
      }
      if (seen.has(dependency)) continue;
      seen.add(dependency);
      walk(dependency, [...path, dependency]);
    }
  };
  walk(start, [start]);
  return seen;
}

// The contract for one workflow file. `errors` are ordered by job so a failure
// names the publisher, not just the graph.
export function releaseGateContractErrors(path, text) {
  let document;
  try {
    document = parseWorkflowYaml(text);
  } catch (error) {
    return [`${path}: could not be parsed: ${error.message}`];
  }
  const jobs = document?.jobs ?? {};
  const errors = [];

  for (const [name, job] of Object.entries(jobs)) {
    for (const dependency of jobNeeds(job)) {
      if (!(dependency in jobs)) {
        errors.push(`${path}: job ${name} needs ${dependency}, which is not a job here`);
      }
    }
  }
  if (errors.length) return errors;

  const publishers = Object.entries(jobs)
    .map(([name, job]) => [name, publicationReasons(job, document?.permissions)])
    .filter(([, reasons]) => reasons.length > 0);
  if (publishers.length === 0) return [];

  const aggregate = jobs[AGGREGATE_JOB];
  if (!aggregate) {
    for (const [name, reasons] of publishers) {
      errors.push(
        `${path}: job ${name} writes artifacts (${reasons[0]}) but this workflow has no ` +
          `${AGGREGATE_JOB} job to depend on`,
      );
    }
    return errors;
  }

  const aggregateIf = String(aggregate.if ?? "");
  if (!/\balways\s*\(\s*\)/.test(aggregateIf)) {
    errors.push(
      `${path}: job ${AGGREGATE_JOB} must run with always(), or a failed gate would skip it ` +
        "instead of failing it",
    );
  }
  const runsDecision = (aggregate.steps ?? []).some((step) =>
    /release-gates\.mjs\s+decide\b/.test(String(step?.run ?? "")),
  );
  if (!runsDecision) {
    errors.push(
      `${path}: job ${AGGREGATE_JOB} must run \`release-gates.mjs decide\`; with always() the ` +
        "job would otherwise succeed no matter how its dependencies ended",
    );
  }

  const aggregateNeeds = new Set(jobNeeds(aggregate));
  for (const gate of REQUIRED_GATES) {
    if (!(gate in jobs)) {
      errors.push(`${path}: required gate ${gate} is not a job in this workflow`);
    } else if (!aggregateNeeds.has(gate)) {
      errors.push(`${path}: required gate ${gate} is not a dependency of ${AGGREGATE_JOB}`);
    }
  }

  for (const [name, reasons] of publishers) {
    if (name === AGGREGATE_JOB) {
      errors.push(`${path}: job ${AGGREGATE_JOB} must not write artifacts (${reasons[0]})`);
      continue;
    }
    let closure;
    try {
      closure = needsClosure(name, jobs);
    } catch (error) {
      errors.push(`${path}: job ${name}: ${error.message}`);
      continue;
    }
    if (!closure.has(AGGREGATE_JOB)) {
      errors.push(
        `${path}: job ${name} writes artifacts (${reasons.join("; ")}) but neither directly nor ` +
          `transitively depends on ${AGGREGATE_JOB}`,
      );
    }
  }
  return errors;
}

export function releaseGateGraphErrors(repositoryRoot = REPOSITORY_ROOT) {
  const workflows = join(repositoryRoot, ".github", "workflows");
  if (!existsSync(workflows)) return [`.github/workflows is missing under ${repositoryRoot}`];
  const errors = [];
  for (const file of readdirSync(workflows).sort()) {
    if (!/\.ya?ml$/.test(file)) continue;
    const path = `.github/workflows/${file}`;
    errors.push(...releaseGateContractErrors(path, readFileSync(join(workflows, file), "utf8")));
  }
  return errors;
}

function check() {
  const errors = releaseGateGraphErrors();
  if (errors.length) {
    console.error("release-gates: the workflow graph does not gate every publication:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} release-orchestration defect(s). Every artifact-writing job must ` +
        `depend on ${AGGREGATE_JOB}, and ${AGGREGATE_JOB} on every mandatory gate.`,
    );
    process.exit(1);
  }
  console.log(
    `✓ every artifact-writing job is dominated by ${AGGREGATE_JOB}, which requires all ` +
      `${REQUIRED_GATES.length} mandatory gates.`,
  );
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const command = process.argv[2];
  if (command === "check") check();
  else if (command === "decide") decide();
  else {
    console.error("usage: release-gates.mjs check | decide");
    process.exit(2);
  }
}
