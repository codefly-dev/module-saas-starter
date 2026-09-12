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
// job depends on. It runs past a failed dependency (`!cancelled()`) so it can
// inspect its dependencies' outcomes itself: a bare `needs:` cannot distinguish
// a check that passed from one that never ran, and "skipped" must not read as
// consent to publish.
//
//   node scripts/ci/release-gates.mjs decide   # the aggregate job's verdict
//   node scripts/ci/release-gates.mjs check    # the static workflow contract
//   node scripts/ci/release-gates.mjs contexts # what GitHub requires, reconciled
//   node scripts/ci/release-gates.mjs fleet    # which repositories gate a merge
//
// `decide` is the runtime half: given `toJSON(needs)` it fails unless every
// mandatory gate actually succeeded. `check` is the static half: it parses the
// workflow dependency graph and fails unless every artifact-writing job — in
// any workflow, including one added later — is transitively dominated by the
// aggregate, and the aggregate by every mandatory gate. It also fails unless
// every action the workflows call is pinned to a commit digest, since a
// mutable tag lets whoever can move it rewrite any gate, the aggregate
// included, and unless every job that reports a required context can report it
// from a merge-queue entry as well.

import { execFileSync } from "node:child_process";
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
  "kit-version",
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
// unless the plan job succeeded and said *explicitly* that it found no affected
// service. An absent or unrecognized `has_work` is not a scoping decision — a
// renamed output or a lost `GITHUB_OUTPUT` write would otherwise excuse the
// three heaviest gates on every branch push while this job stayed green.
export function exemptedGates({ results, ref }) {
  if (isReleaseTag(ref)) return [];
  const plan = results[PLAN_JOB];
  if (plan?.result !== "success" || plan?.outputs?.has_work !== "false") return [];
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
  // A green gate that only inspected part of the topology does not authorize a
  // release. `codefly-plan` forces `--all` from the ref, so anything else here
  // means the affected-scoped gates ran against a delta and cannot speak for
  // the services they skipped.
  if (isReleaseTag(ref) && results[PLAN_JOB]?.result === "success") {
    const all = results[PLAN_JOB]?.outputs?.all;
    if (all !== "true") {
      errors.push(
        `${PLAN_JOB}: resolved a scoped plan (all=${all ?? "absent"}) on a release ref; the ` +
          "mandatory gates must verify the full topology",
      );
    }
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
  { on: "run", pattern: /\bannounce-[\w-]+\.mjs\b/, why: "runs a release announcement script" },
  { on: "uses", pattern: /^actions\/attest-build-provenance/, why: "attests artifact provenance" },
  // Actions that publish authenticate with a secret in `with:`, not with the
  // job's `permissions`, so neither check above sees them.
  { on: "uses", pattern: /repository-dispatch/i, why: "dispatches a release event downstream" },
  { on: "uses", pattern: /(?:^|\/)[^/@]*publish[^/@]*(?:@|$)/i, why: "runs a publishing action" },
  {
    on: "uses",
    pattern: /(?:^|\/)[^/@]*(?:gh-release|create-release|release-action|upload-release)[^/@]*(?:@|$)/i,
    why: "runs a release action",
  },
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
  // A job's `permissions:` replaces the workflow default outright, and either
  // may use GitHub's scalar form — `write-all` grants every scope below, so it
  // must not fall through the object branch untested.
  const permissions = job?.permissions ?? workflowPermissions;
  if (permissions === "write-all") {
    reasons.push("it holds every write permission (permissions: write-all)");
  } else if (permissions && typeof permissions === "object") {
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

// One parse, reported once. An unreadable workflow is a single defect however
// many contracts would have gone on to read it.
function parseWorkflow(path, text) {
  try {
    return { document: parseWorkflowYaml(text) };
  } catch (error) {
    return { error: `${path}: could not be parsed: ${error.message}` };
  }
}

// The contract for one workflow file. `errors` are ordered by job so a failure
// names the publisher, not just the graph.
export function releaseGateContractErrors(path, text) {
  const { document, error } = parseWorkflow(path, text);
  return error ? [error] : contractErrors(path, document);
}

function contractErrors(path, document) {
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

  // `always()` and `!cancelled()` both reach the job when a dependency failed,
  // which is the property that matters. `!cancelled()` additionally skips a
  // superseded run instead of reddening it, so the contract accepts either.
  const aggregateIf = String(aggregate.if ?? "");
  if (!/\balways\s*\(\s*\)/.test(aggregateIf) && !/!\s*cancelled\s*\(\s*\)/.test(aggregateIf)) {
    errors.push(
      `${path}: job ${AGGREGATE_JOB} must run with always() or !cancelled(), or a failed gate ` +
        "would skip it instead of failing it",
    );
  }
  const runsDecision = (aggregate.steps ?? []).some((step) =>
    /release-gates\.mjs\s+decide\b/.test(String(step?.run ?? "")),
  );
  if (!runsDecision) {
    errors.push(
      `${path}: job ${AGGREGATE_JOB} must run \`release-gates.mjs decide\`; running past a ` +
        "failed dependency, the job would otherwise succeed no matter how it ended",
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

// ---------------------------------------------------------------------------
// the merge-queue contract
// ---------------------------------------------------------------------------

// `main` merges through a merge queue: GitHub builds each entry as `main + the
// pull request` and gates the merge on the required contexts reported from
// THAT ref, which is the guarantee "require branches to be up to date" bought
// by making every author rebase by hand and re-run everything.
//
// What that costs is a prerequisite no pull request can reveal, because the
// same job is green there: every required context must report on `merge_group`
// too. One that does not leaves its entry waiting on a check that never
// arrives until the queue evicts it — and since entries merge in order, a
// single missing context stalls every merge in the repository.

// The jobs that report a required context: every mandatory gate, plus the
// aggregate that authorizes publication.
export const QUEUED_CONTEXTS = [...REQUIRED_GATES, AGGREGATE_JOB];

// What those jobs are called where the ruleset names them. A required context
// is matched by a job's `name:`, not by its id, so renaming a job here without
// renaming it in the ruleset leaves the ruleset waiting on a context nothing
// reports — which blocks every pull request and stalls the queue, and is
// invisible in a diff that only shows a tidier job name. Declaring the names
// makes that rename fail here instead.
//
// This list is the tree's copy of a set that really lives in the branch
// ruleset, and nothing in a workflow run can read that. `contexts` below
// reconciles the two against the live API; run it whenever either side moves.
export const REQUIRED_CONTEXTS = [
  "Authorization coverage",
  "Base manifest integrity",
  "Codefly build",
  "Codefly CI plan",
  "Codefly quality",
  "Codefly SDK boundary",
  "Codefly supply chain",
  "Frontend kit version",
  "Immutable module package",
  "Interface docs and story tests",
  "Marketing isolation",
  "Provider setup shims",
  "Release gate contract",
  "Release gates",
];

const MERGE_GROUP_BASE = "github.event.merge_group.base_sha";

// Whether `on` names `event`, in any of the three spellings GitHub accepts: a
// mapping whose key carries no value (`merge_group:`), a sequence, or a bare
// scalar.
function triggersOn(on, event) {
  if (typeof on === "string") return on === event;
  if (Array.isArray(on)) return on.includes(event);
  return typeof on === "object" && on !== null && event in on;
}

// Every scalar anywhere under `value`, so a contract can ask what a job's text
// mentions without knowing whether it sits in `env`, a `run` body or `with`.
function scalars(value) {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.flatMap(scalars);
  if (value !== null && typeof value === "object") return Object.values(value).flatMap(scalars);
  return [];
}

// Whether the job actually *reads* the queue entry's base, as opposed to
// merely mentioning it. Two things separate the two, and the workflow this
// gate guards has both within three lines of each other: block-scalar `run`
// bodies reach here raw, comments and all, and prose about the merge queue
// naturally names the very expression under test. So comment lines are dropped
// first, and what remains counts only inside a `${{ … }}` interpolation, which
// is the only place the expression has any effect.
const uncommented = (text) =>
  text
    .split("\n")
    .filter((line) => !/^\s*#/.test(line))
    .join("\n");

const readsMergeGroupBase = (job) =>
  scalars(job).some((text) =>
    new RegExp(`\\$\\{\\{[^}]*${MERGE_GROUP_BASE.replace(/\./g, "\\.")}`).test(uncommented(text)),
  );

// Whether `cancel-in-progress` cancels a merge-queue entry. `true` cancels a
// superseded run whatever event produced it, and an entry superseded by the
// next one then reports nothing at all, which stalls the queue exactly as
// never having run would. An expression is the author discriminating by event
// and is taken at its word; the bare literal is the copy-paste that is not.
const cancelsEverything = (concurrency) => {
  const cancel = concurrency?.["cancel-in-progress"];
  return cancel !== undefined && String(cancel).trim() === "true";
};

function mergeQueueErrors(path, document) {
  const jobs = document?.jobs ?? {};
  const required = QUEUED_CONTEXTS.filter((context) => context in jobs);
  if (required.length === 0) return [];

  const subject = required.length === 1 ? `job ${required[0]} reports` : `jobs ${required.join(", ")} report`;
  if (!triggersOn(document?.on, "merge_group")) {
    // The rest of the contract is moot without the trigger: a workflow that
    // never runs on `merge_group` has no entry to cancel and no entry base to
    // scope a plan against. One defect, not three.
    return [
      `${path}: ${subject} a required context but this workflow has no \`merge_group:\` ` +
        "trigger; a queued pull request would wait on checks that never report and be evicted",
    ];
  }

  const errors = [];

  if (cancelsEverything(document?.concurrency)) {
    errors.push(
      `${path}: concurrency.cancel-in-progress is unconditionally true, so a superseded ` +
        "merge_group run is cancelled and never reports its required checks; scope it to the " +
        "events whose runs are free to supersede",
    );
  }

  // A job carries its own `concurrency:` independently of the workflow's, and
  // cancelling there stalls the queue just as surely.
  for (const name of required) {
    if (cancelsEverything(jobs[name]?.concurrency)) {
      errors.push(
        `${path}: job ${name} sets concurrency.cancel-in-progress to an unconditional true, so ` +
          "a superseded merge_group run is cancelled and never reports this required context",
      );
    }
  }

  // A queue entry's base is the tip of `main` it was built on, so the delta
  // against it is exactly what the merge would add. The `merge_group` payload
  // carries neither `pull_request.base.sha` nor `before`, so a plan that never
  // reads this silently falls through to the full topology on every entry.
  if (PLAN_JOB in jobs && !readsMergeGroupBase(jobs[PLAN_JOB])) {
    errors.push(
      `${path}: job ${PLAN_JOB} never reads ${MERGE_GROUP_BASE} in an expression, so a queue ` +
        "entry has no base to scope its plan against and would verify the full topology every time",
    );
  }

  // The ruleset matches a required context by a job's `name:`, so a job that
  // reports one and is renamed here stops reporting it there.
  for (const name of required) {
    const context = jobs[name]?.name;
    if (context === undefined) {
      errors.push(
        `${path}: job ${name} reports a required context but has no \`name:\`, so the ruleset ` +
          `has no context to match; give it one of: ${REQUIRED_CONTEXTS.join(", ")}`,
      );
    } else if (!REQUIRED_CONTEXTS.includes(context)) {
      errors.push(
        `${path}: job ${name} is named "${context}", which is not a context the ruleset ` +
          "requires; renaming a required job leaves the ruleset waiting on a context nothing " +
          "reports. Update REQUIRED_CONTEXTS and the ruleset together, then run " +
          "`release-gates.mjs contexts`",
      );
    }
  }

  return errors;
}

// The contexts `document`'s jobs report, for the graph-level check that every
// declared one is actually produced somewhere.
function producedContexts(document) {
  const jobs = document?.jobs ?? {};
  return QUEUED_CONTEXTS.filter((context) => context in jobs)
    .map((name) => jobs[name]?.name)
    .filter((context) => context !== undefined);
}

export function mergeQueueContractErrors(path, text) {
  const { document, error } = parseWorkflow(path, text);
  return error ? [error] : mergeQueueErrors(path, document);
}

// ---------------------------------------------------------------------------
// the action-pinning contract
// ---------------------------------------------------------------------------

// A `uses:` ref that names anything but a digest resolves at run time to
// whatever the tag or branch points at then. That is a standing write into
// every gate in this file — including the aggregate that authorizes
// publication — by whoever can move it upstream.
//
// GitHub resolves a ref in one of two vocabularies and each has its own
// immutable spelling: a git ref pins to a full commit digest, a container
// image to an image digest. Checking only the first would reject a correctly
// pinned `docker://` step and demand a commit digest that does not exist for
// it. Case is not the property under test — an uppercase digest names exactly
// one commit — so only immutability is enforced here.
const COMMIT_DIGEST = /@[0-9a-fA-F]{40}$/;
const IMAGE_DIGEST = /@sha256:[0-9a-fA-F]{64}$/;

// How to pin `ref`, or null when it already is. An action from this repository
// is as trustworthy as the tree that calls it, so a `./` path needs no digest.
function unpinnedRemedy(ref) {
  if (ref.startsWith("./")) return null;
  if (ref.startsWith("docker://")) {
    return IMAGE_DIGEST.test(ref) ? null : "pin it to an image digest (@sha256: and 64 hex characters)";
  }
  return COMMIT_DIGEST.test(ref)
    ? null
    : "pin it to the 40-character commit digest the ref resolves to and keep the version in a trailing comment";
}

// Every `uses:` in one workflow, as [job, ref]. A job carries one directly when
// it calls a reusable workflow, which is as capable as any step it would run.
function usedActionRefs(jobs) {
  const refs = [];
  for (const [name, job] of Object.entries(jobs)) {
    const uses = [job?.uses, ...(job?.steps ?? []).map((step) => step?.uses)];
    for (const ref of uses) if (typeof ref === "string") refs.push([name, ref]);
  }
  return refs;
}

export function actionPinErrors(path, text) {
  const { document, error } = parseWorkflow(path, text);
  return error ? [error] : pinErrors(path, document);
}

function pinErrors(path, document) {
  const errors = [];
  for (const [name, ref] of usedActionRefs(document?.jobs ?? {})) {
    const remedy = unpinnedRemedy(ref);
    if (remedy === null) continue;
    errors.push(`${path}: job ${name} uses ${ref}, whose ref is mutable; ${remedy}`);
  }
  return errors;
}

// A digest is immutable but says nothing: `@9c091bb…` records no release. The
// remedy above has always asked for the version in a trailing comment and
// nothing enforced it, which cost nothing while every pin was moved by hand.
// The `github-actions` entry in .github/dependabot.yml now rewrites these pins
// on a schedule, and the comment it carries alongside is the only human-readable
// record of what a digest means — an unenforced convention would decay silently
// into 40 hex characters nobody can review.
//
// The comment is stripped before the document is parsed, so this reads the raw
// line; the parsed refs are the filter that keeps a `uses:` inside a `run:`
// block scalar from being mistaken for a step.
const USES_LINE = /^[ \t]*(?:-[ \t]+)?uses:[ \t]*(?<ref>[^\s#]+)(?<rest>.*)$/;

function commentErrors(path, document, text) {
  const declared = new Set(usedActionRefs(document?.jobs ?? {}).map(([, ref]) => ref));
  const errors = [];
  text.split("\n").forEach((line, index) => {
    const match = USES_LINE.exec(line);
    if (!match) return;
    const { ref, rest } = match.groups;
    if (!declared.has(ref)) return;
    // A `./` path is versioned by the tree that calls it, exactly as the pin
    // rule reasons, so it has no release to name.
    if (ref.startsWith("./")) return;
    // Only an already-immutable ref is in scope: a mutable one is the pin
    // rule's defect, and its remedy already asks for the comment, so reporting
    // it here too would describe one fix as two.
    if (unpinnedRemedy(ref) !== null) return;
    if (/#\s*\S/.test(rest)) return;
    errors.push(
      `${path}:${index + 1}: uses ${ref} with no trailing version comment; keep the version there ` +
        "so the digest stays reviewable when Dependabot moves it",
    );
  });
  return errors;
}

export function actionCommentErrors(path, text) {
  const { document, error } = parseWorkflow(path, text);
  return error ? [error] : commentErrors(path, document, text);
}

// Every workflow in `repositoryRoot`, parsed once, as {path, document, error}.
function parsedWorkflows(repositoryRoot) {
  const workflows = join(repositoryRoot, ".github", "workflows");
  if (!existsSync(workflows)) return [];
  return readdirSync(workflows)
    .sort()
    .filter((file) => /\.ya?ml$/.test(file))
    .map((file) => ({
      path: `.github/workflows/${file}`,
      text: readFileSync(join(workflows, file), "utf8"),
      ...parseWorkflow(`.github/workflows/${file}`, readFileSync(join(workflows, file), "utf8")),
    }));
}

export function releaseGateGraphErrors(repositoryRoot = REPOSITORY_ROOT) {
  if (!existsSync(join(repositoryRoot, ".github", "workflows"))) {
    return [`.github/workflows is missing under ${repositoryRoot}`];
  }
  const errors = [];
  for (const { path, document, error, text } of parsedWorkflows(repositoryRoot)) {
    if (error) {
      errors.push(error);
      continue;
    }
    errors.push(
      ...contractErrors(path, document),
      ...pinErrors(path, document),
      ...mergeQueueErrors(path, document),
      ...commentErrors(path, document, text),
    );
  }
  return errors;
}

// A declared context that nothing reports is the same stall as one that never
// runs on `merge_group`: the ruleset waits on it forever. Deleting a required
// job, or moving it to a workflow the walk cannot see, lands here.
//
// This is separate from the walk above because it is a claim about *this*
// repository's ruleset, not about any tree of workflows — a fixture root is
// entitled to hold one workflow and no required context at all.
export function unreportedContextErrors(repositoryRoot = REPOSITORY_ROOT) {
  const produced = [];
  for (const { document } of parsedWorkflows(repositoryRoot)) {
    if (document) produced.push(...producedContexts(document));
  }
  return REQUIRED_CONTEXTS.filter((context) => !produced.includes(context)).map(
    (context) =>
      `no job in .github/workflows reports the required context "${context}"; the ruleset would ` +
      "wait on it forever. Remove it from REQUIRED_CONTEXTS and the ruleset together, then run " +
      "`release-gates.mjs contexts`",
  );
}

// ---------------------------------------------------------------------------
// the live half — what GitHub actually gates a merge on
// ---------------------------------------------------------------------------

// `check` can only hold the workflows to what this tree declares. The set that
// actually gates a merge lives in GitHub's own branch configuration, which a
// workflow run cannot read: it needs `Administration: read`, and the token a
// job gets tops out below that. So this half runs from a developer's or agent's
// own credentials rather than in CI, and is the thing to run whenever either
// side moves.

// `gh api` exits nonzero on any HTTP error but still writes the response body,
// which carries the status, to stdout. Keeping that status is the whole point:
// a 404 from the branch-protection endpoint means the branch is unprotected,
// while a 403 means the answer was withheld, and reading the second as the
// first reports a repository as ungated when checks do gate it.
export function ghJson(endpoint, extraArgs = []) {
  const options = { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], maxBuffer: 64 * 1024 * 1024 };
  try {
    return { status: 200, body: JSON.parse(execFileSync("gh", ["api", ...extraArgs, endpoint], options)) };
  } catch (error) {
    let body;
    try {
      body = JSON.parse(error.stdout);
    } catch {
      throw error;
    }
    if (!body?.status) throw error;
    return { status: Number(body.status), body };
  }
}

const BRANCH_UNPROTECTED = 404;

// Every way a status check can be required on one branch. The two mechanisms
// have to be read separately because neither endpoint reports the other's
// answer: `rules/branches` returns `[]` for a branch whose merges are gated by
// classic protection, and `branches/*/protection` 404s "Branch not protected"
// for a branch gated by a ruleset. Consulting one alone is how an audit calls a
// gated repository advisory.
//
// A read that failed is the third answer, and it is kept rather than collapsed:
// a branch whose configuration could not be read is neither gated nor
// advisory, and recording it as advisory is the same mistake in the direction
// that hides work.
export function branchGating(repository, branch, gh = ghJson) {
  const contexts = new Set();
  const mechanisms = new Set();
  const unreadable = [];
  let strict = false;
  let queue = false;

  const rules = gh(`repos/${repository}/rules/branches/${branch}`);
  if (rules.status === 200) {
    for (const rule of rules.body) {
      if (rule.type === "merge_queue") queue = true;
      if (rule.type !== "required_status_checks") continue;
      mechanisms.add("ruleset");
      strict ||= rule.parameters?.strict_required_status_checks_policy === true;
      for (const check of rule.parameters?.required_status_checks ?? []) contexts.add(check.context);
    }
  } else {
    unreadable.push({ endpoint: `rules/branches/${branch}`, status: rules.status, message: rules.body.message });
  }

  const protection = gh(`repos/${repository}/branches/${branch}/protection`);
  if (protection.status === 200) {
    const required = protection.body.required_status_checks;
    if (required) {
      mechanisms.add("branch protection");
      strict ||= required.strict === true;
      for (const check of required.checks) contexts.add(check.context);
    }
  } else if (protection.status !== BRANCH_UNPROTECTED) {
    unreadable.push({
      endpoint: `branches/${branch}/protection`,
      status: protection.status,
      message: protection.body.message,
    });
  }

  return { contexts: [...contexts].sort(), strict, queue, mechanisms: [...mechanisms], unreadable };
}

// Neither branch protection nor rulesets exist for a private repository on a
// free account: both endpoints answer 403. That is an answer — nothing can
// require a check there until the repository is public or the account is
// upgraded — and it is not the same as a read this credential merely was not
// allowed to make, which leaves the sweep incomplete and must say so.
export function gatingVerdict({ private: isPrivate, contexts, unreadable }) {
  if (unreadable.length) {
    return isPrivate && unreadable.every(({ status }) => status === 403) ? "unavailable" : "unknown";
  }
  return contexts.length ? "gated" : "advisory";
}

function repositoryFacts(repository, gh = ghJson) {
  const { body } = gh(`repos/${repository}`);
  return { branch: body.default_branch, private: body.private };
}

// ---------------------------------------------------------------------------
// contexts — reconcile REQUIRED_CONTEXTS with what gates this repository
// ---------------------------------------------------------------------------

// A context required on the branch but absent from `REQUIRED_CONTEXTS` is the
// dangerous direction: nothing makes it run on `merge_group`, so the first
// queued pull request waits out the queue's check-response timeout and is
// evicted, and every merge behind it stalls.

// What the two sides disagree about, as [missingHere, missingThere].
export function contextDrift(declared, required) {
  return [
    required.filter((context) => !declared.includes(context)),
    declared.filter((context) => !required.includes(context)),
  ];
}

function contexts(repository) {
  const facts = repositoryFacts(repository);
  const gating = branchGating(repository, facts.branch);
  const verdict = gatingVerdict({ ...facts, ...gating });
  if (verdict === "unknown") {
    console.error(`contexts: ${repository}'s configuration on ${facts.branch} could not be read:`);
    gating.unreadable.forEach(({ endpoint, status, message }) =>
      console.error(`    ${endpoint}: ${status} ${message ?? ""}`.trimEnd()),
    );
    process.exit(1);
  }
  if (verdict === "unavailable") {
    console.error(
      `contexts: ${repository} is private on an account whose plan offers neither branch protection ` +
        "nor rulesets, so no check can be required there and a merge queue would gate nothing.",
    );
    process.exit(1);
  }
  if (verdict === "advisory") {
    console.error(
      `contexts: nothing requires a status check on ${repository}'s ${facts.branch}, so a red run ` +
        "blocks no merge there and a merge queue would gate nothing.",
    );
    process.exit(1);
  }
  const required = gating.contexts;
  const [unguarded, stale] = contextDrift(REQUIRED_CONTEXTS, required);
  for (const context of unguarded) {
    console.error(
      `    ${facts.branch} requires "${context}", which REQUIRED_CONTEXTS does not declare; nothing ` +
        "holds it to running on merge_group, so a queue entry can wait on it forever",
    );
  }
  for (const context of stale) {
    console.error(
      `    REQUIRED_CONTEXTS declares "${context}", which ${facts.branch} does not require; either ` +
        "the ruleset lost it or this list is stale",
    );
  }
  if (unguarded.length || stale.length) {
    console.error(`\nFAIL: ${unguarded.length + stale.length} context(s) differ between ${repository} and this tree.`);
    process.exit(1);
  }
  console.log(
    `✓ ${repository}'s ${facts.branch} requires exactly the ${required.length} contexts ` +
      `REQUIRED_CONTEXTS declares, via ${gating.mechanisms.join(" + ")}.`,
  );
}

// ---------------------------------------------------------------------------
// fleet — which repositories in the organization block a merge at all
// ---------------------------------------------------------------------------

// #611 asked to roll this repository's merge queue out to "every active repo in
// the org that has required status checks on its default branch". Answering
// that means enumerating the organization rather than sweeping a hand-written
// list of repository names: the list that answered it in #616 held 17 of them,
// and a list of names cannot report what it never looked at — it read as a
// complete audit while missing the one other repository that does require
// checks.
export function activeRepositories(owner, gh = ghJson) {
  const pages = gh(`orgs/${owner}/repos?per_page=100`, ["--paginate", "--slurp"]);
  return pages.body
    .flat()
    .filter((repository) => !repository.archived)
    .map(({ full_name, default_branch, private: isPrivate }) => ({
      repository: full_name,
      branch: default_branch,
      private: isPrivate,
    }))
    .sort((left, right) => left.repository.localeCompare(right.repository));
}

// One verdict line per repository, greppable, and the context names under each
// repository that gates something — those are the audit, and the set a rollout
// would have to hold to `merge_group`.
export function fleetLines(rows) {
  const width = Math.max(...rows.map((row) => row.repository.length));
  const name = (row) => `${row.repository.padEnd(width)}  ${row.branch}`;
  const lines = [];
  for (const row of rows) {
    const verdict = gatingVerdict(row);
    if (verdict === "unknown") {
      const reads = row.unreadable.map(
        ({ endpoint, status, message }) => `${endpoint}: ${status} ${message ?? ""}`.trimEnd(),
      );
      lines.push(`unknown      ${name(row)}  ${reads.join("; ")}`);
      continue;
    }
    if (verdict === "unavailable") {
      lines.push(`unavailable  ${name(row)}  private on a plan with neither branch protection nor rulesets`);
      continue;
    }
    if (verdict === "advisory") {
      lines.push(`advisory     ${name(row)}  nothing requires a status check`);
      continue;
    }
    const detail = [`${row.contexts.length} required via ${row.mechanisms.join(" + ")}`];
    detail.push(row.queue ? "merge queue" : "no merge queue");
    if (row.strict) detail.push("strict up-to-date");
    lines.push(`gated        ${name(row)}  ${detail.join(", ")}`);
    row.contexts.forEach((context) => lines.push(`                 "${context}"`));
  }
  return lines;
}

function fleet(owner) {
  const rows = activeRepositories(owner).map((facts) => ({
    ...facts,
    ...branchGating(facts.repository, facts.branch),
  }));
  fleetLines(rows).forEach((line) => console.log(line));
  const count = (verdict) => rows.filter((row) => gatingVerdict(row) === verdict).length;
  const queued = rows.filter((row) => gatingVerdict(row) === "gated" && row.queue).length;
  console.log(
    `\n${rows.length} active repositories in ${owner}: ${count("gated")} gate a merge on a status check ` +
      `(${queued} through a merge queue), ${count("advisory")} are advisory, ${count("unavailable")} cannot ` +
      `require one on this plan, ${count("unknown")} could not be read.`,
  );
  if (count("unknown")) {
    console.error(
      `\nFAIL: ${count("unknown")} of them withheld their branch configuration, so this sweep is not the ` +
        "complete audit it would otherwise read as. Re-run with credentials that can read them.",
    );
    process.exit(1);
  }
}

function check() {
  const errors = [...releaseGateGraphErrors(), ...unreportedContextErrors()];
  if (errors.length) {
    console.error("release-gates: the workflows do not satisfy the release contract:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} workflow-contract defect(s). Every artifact-writing job must ` +
        `depend on ${AGGREGATE_JOB}, ${AGGREGATE_JOB} on every mandatory gate, every action on a ` +
        "digest carrying a version comment, and every context in REQUIRED_CONTEXTS must be reported, under that name, by a " +
        "job that runs on merge_group.",
    );
    process.exit(1);
  }
  console.log(
    `✓ every artifact-writing job is dominated by ${AGGREGATE_JOB}, which requires all ` +
      `${REQUIRED_GATES.length} mandatory gates; every action is pinned to a commented digest; each of ` +
      `the ${REQUIRED_CONTEXTS.length} contexts declared in REQUIRED_CONTEXTS is reported, ` +
      "under that name, by a job that runs on merge_group.",
  );
  console.log(
    "  note: that the ruleset requires exactly those contexts is not checked here — no token " +
      "in a workflow run can read it. Run `release-gates.mjs contexts` to reconcile the two.",
  );
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const command = process.argv[2];
  if (command === "check") check();
  else if (command === "decide") decide();
  else if (command === "contexts") contexts(process.argv[3] ?? "codefly-dev/module-saas-starter");
  else if (command === "fleet") fleet(process.argv[3] ?? "codefly-dev");
  else {
    console.error("usage: release-gates.mjs check | decide | contexts [owner/repo] | fleet [owner]");
    process.exit(2);
  }
}
