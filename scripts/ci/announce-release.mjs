#!/usr/bin/env node
// announce-release — tell the handbook repository that a new interface is current.
//
// The dispatch is outward-facing (hence its place behind `release-gates`) but it
// is not a publication of *this* release: nothing here is retracted when it does
// not happen. A repository that never provisioned HANDBOOK_REPOSITORY and
// HANDBOOK_DISPATCH_TOKEN has no handbook to announce to, so treating that as a
// release failure marks a correct, fully published release red beside the
// failures that genuinely mean "the artifacts are wrong".
//
// A half-provisioned pair is the opposite case: someone meant this to work, and
// staying quiet would leave every later release silently unannounced. That fails
// loudly.

import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);

// The artifact the handbook renders the functional page from.
const SURFACE_ARTIFACT = "module/services/accounts/generated/service-catalog.json";

export function announcementPlan({ handbook, token }) {
  const missing = [
    ["HANDBOOK_REPOSITORY", handbook],
    ["HANDBOOK_DISPATCH_TOKEN", token],
  ]
    .filter(([, value]) => !value)
    .map(([name]) => name);
  if (missing.length === 2) return { action: "skip", missing };
  if (missing.length === 1) return { action: "fail", missing };
  return { action: "dispatch", missing };
}

// The payload names the exact commit, and the consumer must render from it.
// Resolving "the latest release" instead would read a stale interface: this
// track publishes tags, and a tag does not create a GitHub Release, so the
// newest release object here can be many releases behind the tag being
// announced.
export function dispatchArguments({ handbook, repository, release, commit }) {
  return [
    "api",
    `repos/${handbook}/dispatches`,
    "-f",
    "event_type=surface-bump",
    "-f",
    `client_payload[repository]=${repository}`,
    "-f",
    `client_payload[release]=${release}`,
    "-f",
    `client_payload[commit]=${commit}`,
    "-f",
    `client_payload[artifact]=${SURFACE_ARTIFACT}`,
  ];
}

function main() {
  const handbook = process.env.HANDBOOK_REPOSITORY ?? "";
  const plan = announcementPlan({ handbook, token: process.env.GH_TOKEN ?? "" });
  if (plan.action === "skip") {
    console.log(
      "::notice::no handbook is configured on this repository, so the release was not " +
        "announced. Set the HANDBOOK_REPOSITORY repository variable (owner/repo) and the " +
        "HANDBOOK_DISPATCH_TOKEN secret to announce it.",
    );
    return;
  }
  if (plan.action === "fail") {
    console.error(
      `::error::the release was not announced: ${plan.missing[0]} is unset while the other ` +
        "half of the handbook configuration is present. Provision both, or neither.",
    );
    process.exit(1);
  }
  const release = process.env.GITHUB_REF_NAME;
  execFileSync(
    "gh",
    dispatchArguments({
      handbook,
      repository: process.env.GITHUB_REPOSITORY,
      release,
      commit: process.env.GITHUB_SHA,
    }),
    { stdio: "inherit" },
  );
  console.log(`✓ announced ${release} to ${handbook}`);
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
