#!/usr/bin/env node
// announce-release — tell the handbook repository that a new interface is current.
//
// The dispatch is outward-facing (hence its place behind `release-gates`) but it
// is not a publication of *this* release: every artifact is already public by
// the time it runs, and nothing is retracted when it does not happen. So an
// absent handbook is a fact about this repository's configuration, not a failed
// release — it warns and succeeds. Failing instead marks a correct release red
// beside the failures that genuinely mean "the artifacts are wrong" (#551).
//
// A half-provisioned pair warns on the same footing, and is the likeliest way to
// be unconfigured: the variable and the secret are two separate operator
// commands, so the window between them is a normal state, not a broken one. The
// warning is an annotation on every release run until both are set.

import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);

// The artifact the handbook renders the functional page from.
const SURFACE_ARTIFACT = "module/services/accounts/generated/service-catalog.json";

function main() {
  const handbook = process.env.HANDBOOK_REPOSITORY ?? "";
  const missing = [
    ["HANDBOOK_REPOSITORY", handbook],
    ["HANDBOOK_DISPATCH_TOKEN", process.env.GH_TOKEN ?? ""],
  ]
    .filter(([, value]) => !value)
    .map(([name]) => name);
  if (missing.length) {
    console.log(
      `::warning::the release was not announced: ${missing.join(" and ")} unset on this ` +
        "repository. The release itself is unaffected — set both to announce it.",
    );
    return;
  }
  // The payload names the exact commit, and the consumer must render from it.
  // Resolving "the latest release" instead would read a stale interface: this
  // track publishes tags, and a tag does not create a GitHub Release, so the
  // newest release object here can be many releases behind the tag being
  // announced.
  execFileSync(
    "gh",
    [
      "api",
      `repos/${handbook}/dispatches`,
      "-f",
      "event_type=surface-bump",
      "-f",
      `client_payload[repository]=${process.env.GITHUB_REPOSITORY}`,
      "-f",
      `client_payload[release]=${process.env.GITHUB_REF_NAME}`,
      "-f",
      `client_payload[commit]=${process.env.GITHUB_SHA}`,
      "-f",
      `client_payload[artifact]=${SURFACE_ARTIFACT}`,
    ],
    { stdio: "inherit" },
  );
  console.log(`✓ announced ${process.env.GITHUB_REF_NAME} to ${handbook}`);
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
