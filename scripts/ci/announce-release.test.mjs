import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const SCRIPT = join(import.meta.dirname, "announce-release.mjs");

// A `gh` first on PATH that records its argv and exits with `exit`. Recording is
// what makes "no dispatch was attempted" observable: an unconfigured run that
// dispatched anyway would reach GitHub as `repos//dispatches`, so the absence of
// the log is the assertion that matters, not the absence of an error.
function stubGh({ exit = 0 } = {}) {
  const directory = mkdtempSync(join(tmpdir(), "announce-release-"));
  const log = join(directory, "argv");
  writeFileSync(join(directory, "gh"), `#!/bin/sh\nprintf '%s\\n' "$@" > ${log}\nexit ${exit}\n`);
  chmodSync(join(directory, "gh"), 0o755);
  return {
    directory,
    dispatched: () => existsSync(log),
    argv: () => readFileSync(log, "utf8").trimEnd().split("\n"),
    cleanup: () => rmSync(directory, { recursive: true, force: true }),
  };
}

function run(env, gh) {
  return execFileSync(process.execPath, [SCRIPT], {
    encoding: "utf8",
    env: { PATH: `${gh.directory}:${process.env.PATH}`, ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
}

test("an unprovisioned handbook warns, succeeds, and dispatches nothing", (t) => {
  const gh = stubGh();
  t.after(gh.cleanup);
  const output = run({}, gh);
  assert.match(output, /^::warning::/);
  assert.match(output, /HANDBOOK_REPOSITORY and HANDBOOK_DISPATCH_TOKEN unset/);
  assert.equal(gh.dispatched(), false);
});

// Provisioning is two operator commands, so the window between them is a normal
// state. Failing there would put a correct, fully published release back in the
// red for a docs reason — the whole of #551.
test("half a handbook configuration warns too, and never fails the release", (t) => {
  for (const [env, named] of [
    [{ HANDBOOK_REPOSITORY: "owner/handbook" }, "HANDBOOK_DISPATCH_TOKEN"],
    [{ GH_TOKEN: "t" }, "HANDBOOK_REPOSITORY"],
  ]) {
    const gh = stubGh();
    t.after(gh.cleanup);
    const output = run(env, gh);
    assert.match(output, /^::warning::/);
    assert.match(output, new RegExp(`${named} unset`));
    assert.equal(gh.dispatched(), false);
  }
});

test("a fully provisioned handbook dispatches the announced commit", (t) => {
  const gh = stubGh();
  t.after(gh.cleanup);
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
  assert.deepEqual(gh.argv(), [
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
  ]);
});

test("a dispatch the handbook rejects fails the job", (t) => {
  const gh = stubGh({ exit: 1 });
  t.after(gh.cleanup);
  assert.throws(
    () => run({ HANDBOOK_REPOSITORY: "owner/handbook", GH_TOKEN: "t" }, gh),
    (error) => {
      assert.notEqual(error.status, 0);
      return true;
    },
  );
});
