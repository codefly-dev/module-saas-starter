import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { announcementPlan, dispatchArguments } from "./announce-release.mjs";

const SCRIPT = join(import.meta.dirname, "announce-release.mjs");

// A `gh` first on PATH that records its arguments and exits with `exit`, so the
// dispatch path is exercised without reaching GitHub.
function stubGh({ exit = 0 } = {}) {
  const directory = mkdtempSync(join(tmpdir(), "announce-release-"));
  const log = join(directory, "args");
  writeFileSync(
    join(directory, "gh"),
    `#!/bin/sh\nprintf '%s\\n' "$@" > ${log}\nexit ${exit}\n`,
  );
  chmodSync(join(directory, "gh"), 0o755);
  return { directory, arguments: () => readFileSync(log, "utf8").trimEnd().split("\n") };
}

function run(env, gh = stubGh()) {
  return execFileSync(process.execPath, [SCRIPT], {
    encoding: "utf8",
    env: { PATH: `${gh.directory}:${process.env.PATH}`, ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
}

test("an unprovisioned handbook skips the announcement instead of failing the release", () => {
  assert.deepEqual(announcementPlan({ handbook: "", token: "" }), {
    action: "skip",
    missing: ["HANDBOOK_REPOSITORY", "HANDBOOK_DISPATCH_TOKEN"],
  });
  const output = run({});
  assert.match(output, /^::notice::/);
  assert.match(output, /HANDBOOK_REPOSITORY/);
});

test("half a handbook configuration fails, so no later release goes silently unannounced", () => {
  assert.deepEqual(announcementPlan({ handbook: "owner/handbook", token: "" }), {
    action: "fail",
    missing: ["HANDBOOK_DISPATCH_TOKEN"],
  });
  assert.deepEqual(announcementPlan({ handbook: "", token: "t" }), {
    action: "fail",
    missing: ["HANDBOOK_REPOSITORY"],
  });
  for (const env of [{ HANDBOOK_REPOSITORY: "owner/handbook" }, { GH_TOKEN: "t" }]) {
    assert.throws(
      () => run(env),
      (error) => {
        assert.equal(error.status, 1);
        assert.match(error.stderr, /^::error::the release was not announced/);
        return true;
      },
    );
  }
});

test("a fully provisioned handbook is dispatched the announced commit", () => {
  assert.equal(announcementPlan({ handbook: "owner/handbook", token: "t" }).action, "dispatch");
  const expected = [
    "api",
    "repos/owner/handbook/dispatches",
    "-f",
    "event_type=surface-bump",
    "-f",
    "client_payload[repository]=codefly-dev/module-saas-starter",
    "-f",
    "client_payload[release]=v0.0.58",
    "-f",
    "client_payload[commit]=cafe1234",
    "-f",
    "client_payload[artifact]=module/services/accounts/generated/service-catalog.json",
  ];
  assert.deepEqual(
    dispatchArguments({
      handbook: "owner/handbook",
      repository: "codefly-dev/module-saas-starter",
      release: "v0.0.58",
      commit: "cafe1234",
    }),
    expected,
  );
  const gh = stubGh();
  run(
    {
      HANDBOOK_REPOSITORY: "owner/handbook",
      GH_TOKEN: "t",
      GITHUB_REPOSITORY: "codefly-dev/module-saas-starter",
      GITHUB_REF_NAME: "v0.0.58",
      GITHUB_SHA: "cafe1234",
    },
    gh,
  );
  assert.deepEqual(gh.arguments(), expected);
});

test("a dispatch the handbook rejects fails the job", () => {
  assert.throws(
    () => run({ HANDBOOK_REPOSITORY: "owner/handbook", GH_TOKEN: "t" }, stubGh({ exit: 1 })),
    (error) => {
      assert.notEqual(error.status, 0);
      return true;
    },
  );
});
