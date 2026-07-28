import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import test from "node:test";

async function readinessReport(catalog: unknown) {
  const server = createServer((_request, response) => {
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify(catalog));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address === "object");
  try {
    const output = await new Promise<string>((resolve, reject) => {
      const child = spawn(process.execPath, ["scripts/readiness.mjs", "--report"], {
        cwd: process.cwd(),
        env: {
          ...process.env,
          MARKETING_CATALOG_URL: `http://127.0.0.1:${address.port}`,
        },
      });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
      });
      child.stderr.on("data", (chunk) => {
        stderr += chunk;
      });
      child.on("error", reject);
      child.on("exit", (code) => {
        if (code === 0) resolve(stdout);
        else reject(new Error(stderr));
      });
    });
    return JSON.parse(output);
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
}

test("production readiness requires a non-fixture public catalog", async () => {
  const empty = await readinessReport({ revision: "empty", plans: [] });
  assert.equal(empty.checks.pricing, false);
  assert.ok(
    empty.requiredActions.includes(
      "publish at least one authoritative public pricing plan",
    ),
  );

  const configured = await readinessReport({
    revision: "catalog-v1",
    plans: [
      {
        key: "pro",
        currency: "USD",
        amountMinor: 12900,
        checkoutEnabled: true,
        fixture: false,
      },
    ],
  });
  assert.equal(configured.checks.pricing, true);
});
