import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SHIM = join(HERE, "resend.sh");
const SOURCE = readFileSync(SHIM, "utf8");

// Run the shim and capture status + streams without throwing on non-zero exit.
function run(args, { env = {}, cwd } = {}) {
  try {
    const stdout = execFileSync("bash", [SHIM, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      cwd,
      env: { ...process.env, RESEND_API_KEY: "", ...env },
    });
    return { code: 0, stdout, stderr: "" };
  } catch (error) {
    return {
      code: error.status ?? 1,
      stdout: error.stdout?.toString() ?? "",
      stderr: error.stderr?.toString() ?? "",
    };
  }
}

// Run fn with a fresh temp dir that is always removed afterward.
function withTempDir(fn) {
  const dir = mkdtempSync(join(tmpdir(), "resend-shim-"));
  try {
    return fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function withKeyFile(contents, fn) {
  return withTempDir((dir) => {
    const file = join(dir, "key.env");
    writeFileSync(file, contents);
    return fn(file);
  });
}

test("shim never contacts Resend or writes configuration", () => {
  assert.doesNotMatch(SOURCE, /\bcurl\b/, "shim must not curl");
  assert.doesNotMatch(SOURCE, /api\.resend\.com/, "shim must not reference the Resend API");
  assert.doesNotMatch(SOURCE, /request POST|\/webhooks\b/, "shim must not manage webhooks");
  assert.doesNotMatch(SOURCE, /setup_install_pair/, "shim must not install configuration");
  assert.doesNotMatch(SOURCE, /setup_doctor|codefly /, "shim must not run doctor or codefly");
});

test("shim preserves the shared provider-common helpers", () => {
  assert.match(SOURCE, /source "\$\{SCRIPT_DIR\}\/provider-common\.sh"/);
});

test("--help exits cleanly and points at the plugin", () => {
  const { code, stdout } = run(["--help"]);
  assert.equal(code, 0);
  assert.match(stdout, /codefly-dev\/provider-resend/);
});

for (const flag of [
  "--provision-webhook",
  "--skip-remote-validation",
  "--webhook-origin",
  "--webhook-secret-file",
  "--from",
  "--force",
  "--workspace",
  "--skip-doctor",
]) {
  test(`${flag} hard-fails with plugin guidance`, () => {
    // Give value-taking flags an argument so the failure is the removal, not a missing value.
    const { code, stderr } = run([flag, "unused"]);
    assert.equal(code, 1, `${flag} must exit non-zero`);
    assert.match(stderr, /is removed/, `${flag} must say it is removed`);
    assert.match(stderr, /codefly-dev\/provider-resend/, `${flag} must point at the plugin`);
  });
}

test("host classification accepts a well-formed re_ key without echoing it", () => {
  const key = "re_codeflyfixture_ABC123xyz";
  withKeyFile(`${key}\n`, (file) => {
    const { code, stdout } = run(["--api-key-file", file]);
    assert.equal(code, 0, "a well-formed re_ key must be accepted");
    assert.match(stdout, /well-formed Resend API key/);
    assert.doesNotMatch(stdout, new RegExp(key), "the key value must never be echoed");
  });
});

test("a malformed key is refused without being echoed", () => {
  const key = "sk_live_NOTRESEND";
  withKeyFile(`${key}\n`, (file) => {
    const { code, stderr } = run(["--api-key-file", file]);
    assert.equal(code, 1, "a non-re_ value must be refused");
    assert.match(stderr, /well-formed Resend API key/);
    assert.doesNotMatch(stderr, /NOTRESEND/, "the value must never be echoed");
  });
});

test("an ambient malformed RESEND_API_KEY is classified and refused", () => {
  const { code, stdout, stderr } = run([], { env: { RESEND_API_KEY: "garbage_SECRET" } });
  assert.equal(code, 1, "an ambient malformed key must be refused");
  assert.doesNotMatch(stdout + stderr, /SECRET/, "the value must never be echoed");
});

test("--env-file classifies the api key and never handles the webhook secret", () => {
  withKeyFile(
    "RESEND_API_KEY=re_codeflyfixture\nRESEND_WEBHOOK_SECRET=whsec_LEAKME\n",
    (file) => {
      const { code, stdout, stderr } = run(["--env-file", file]);
      assert.equal(code, 0);
      assert.match(stdout, /well-formed Resend API key/);
      assert.doesNotMatch(stdout + stderr, /whsec_LEAKME/, "the webhook secret must never be read or echoed");
    },
  );
});

test("the shim writes nothing to its working directory or TMPDIR", () => {
  withTempDir((sandbox) => {
    withKeyFile("re_codeflyfixture\n", (file) => {
      // The old script mktemp'd a workdir under TMPDIR and wrote email config.
      const { code } = run(["--api-key-file", file], { cwd: sandbox, env: { TMPDIR: sandbox } });
      assert.equal(code, 0);
    });
    assert.deepEqual(readdirSync(sandbox), [], "shim must not create any files while running");
  });
});

test("import guidance is import-by-remote-id, not URL adoption", () => {
  const { stdout } = run([]);
  assert.match(stdout, /import it into/i);
  assert.match(stdout, /remote webhook id/i);
  assert.match(stdout, /not by its endpoint URL/i);
});
