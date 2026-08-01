import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SHIM = join(HERE, "stripe.sh");
const SOURCE = readFileSync(SHIM, "utf8");

// Run the shim and capture status + streams without throwing on non-zero exit.
function run(args, { env = {} } = {}) {
  try {
    const stdout = execFileSync("bash", [SHIM, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, STRIPE_API_KEY: "", ...env },
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

function withKeyFile(contents, fn) {
  const dir = mkdtempSync(join(tmpdir(), "stripe-shim-"));
  const file = join(dir, "key.env");
  writeFileSync(file, contents);
  try {
    return fn(file, dir);
  } finally {
    // Leave the temp dir contents assertable to the caller before cleanup.
    for (const name of readdirSync(dir)) {
      if (name !== "key.env") throw new Error(`shim wrote an unexpected file: ${name}`);
    }
  }
}

test("shim never contacts Stripe or writes configuration", () => {
  assert.doesNotMatch(SOURCE, /\bcurl\b/, "shim must not curl");
  assert.doesNotMatch(SOURCE, /api\.stripe\.com/, "shim must not reference the Stripe API");
  assert.doesNotMatch(SOURCE, /webhook_endpoints/, "shim must not manage webhook endpoints");
  assert.doesNotMatch(SOURCE, /setup_install_pair/, "shim must not install configuration");
  assert.doesNotMatch(SOURCE, /setup_doctor|codefly /, "shim must not run doctor or codefly");
});

test("shim preserves the shared provider-common helpers", () => {
  assert.match(SOURCE, /source "\$\{SCRIPT_DIR\}\/provider-common\.sh"/);
});

test("--help exits cleanly and points at the plugin", () => {
  const { code, stdout } = run(["--help"]);
  assert.equal(code, 0);
  assert.match(stdout, /codefly-dev\/provider-stripe/);
});

for (const flag of [
  "--provision-webhook",
  "--skip-remote-validation",
  "--webhook-origin",
  "--webhook-secret-file",
  "--force",
  "--workspace",
  "--skip-doctor",
]) {
  test(`${flag} hard-fails with plugin guidance`, () => {
    // Give value-taking flags an argument so the failure is the removal, not a missing value.
    const { code, stderr } = run([flag, "unused"]);
    assert.equal(code, 1, `${flag} must exit non-zero`);
    assert.match(stderr, /is removed/, `${flag} must say it is removed`);
    assert.match(stderr, /codefly-dev\/provider-stripe/, `${flag} must point at the plugin`);
  });
}

test("host classification accepts both sk_test_ and rk_test_", () => {
  for (const prefix of ["sk_test_", "rk_test_"]) {
    const key = `${prefix}ABC123xyz`;
    withKeyFile(`${key}\n`, (file) => {
      const { code, stdout } = run(["--api-key-file", file]);
      assert.equal(code, 0, `${prefix} must be accepted`);
      assert.match(stdout, /Recognized a Stripe test-mode key/);
      assert.doesNotMatch(stdout, new RegExp(key), "the key value must never be echoed");
    });
  }
});

test("live-mode keys are refused for both sk_ and rk_ prefixes", () => {
  for (const prefix of ["sk_live_", "rk_live_"]) {
    withKeyFile(`${prefix}SECRET\n`, (file) => {
      const { code, stderr } = run(["--api-key-file", file]);
      assert.equal(code, 1, `${prefix} must be refused`);
      assert.match(stderr, /live-mode key/);
      assert.doesNotMatch(stderr, /SECRET/, "the key value must never be echoed");
    });
  }
});

test("import guidance is import-by-id, not URL adoption", () => {
  const { stdout } = run([]);
  assert.match(stdout, /import it into/i);
  assert.match(stdout, /we_\.\.\./);
  assert.match(stdout, /not by URL/i);
});
