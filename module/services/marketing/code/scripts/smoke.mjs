import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";

const port = 4317;
const origin = `http://127.0.0.1:${port}`;
const child = spawn(process.execPath, [".next/standalone/server.js"], {
  env: {
    ...process.env,
    HOSTNAME: "127.0.0.1",
    MARKETING_CATALOG_URL: "http://localhost:9",
    MARKETING_ENABLED: "true",
    MARKETING_INDEXABLE: "false",
    MARKETING_RELEASE: "smoke-test",
    MARKETING_STRICT_READINESS: "0",
    NODE_ENV: "production",
    PORT: String(port),
  },
  stdio: ["ignore", "pipe", "pipe"],
});

let output = "";
child.stdout.on("data", (chunk) => {
  output += chunk;
});
child.stderr.on("data", (chunk) => {
  output += chunk;
});

async function waitForServer() {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    if (child.exitCode !== null) throw new Error(output);
    try {
      const response = await fetch(`${origin}/api/health`);
      if (response.ok) return;
    } catch {}
    await delay(250);
  }
  throw new Error(`marketing server did not become healthy\n${output}`);
}

try {
  await waitForServer();
  const healthResponse = await fetch(`${origin}/api/health`);
  assert.match(healthResponse.headers.get("cache-control") ?? "", /no-store/);
  const health = await healthResponse.json();
  assert.deepEqual(health, {
    status: "ok",
    service: "marketing",
    release: "smoke-test",
    enabled: true,
  });

  const home = await fetch(origin);
  assert.equal(home.status, 200);
  assert.doesNotMatch(
    home.headers.get("cache-control") ?? "",
    /private|no-store/,
  );
  assert.match(
    home.headers.get("content-security-policy") ?? "",
    /script-src 'self' 'unsafe-inline'/,
  );
  const homeDocument = await home.text();
  assert.match(homeDocument, /One starter\. Two deployables\./);
  assert.match(homeDocument, /<script/);
  assert.match(homeDocument, /property="og:image"/);
  assert.match(homeDocument, /name="twitter:card" content="summary_large_image"/);
  assert.match(homeDocument, /name="twitter:image"/);

  const pricing = await fetch(`${origin}/pricing`);
  assert.equal(pricing.status, 200);
  assert.match(await pricing.text(), /Pricing is temporarily unavailable/);

  const readiness = await fetch(`${origin}/api/readiness`);
  assert.equal(readiness.status, 200);
  assert.match(readiness.headers.get("cache-control") ?? "", /no-store/);
  assert.equal((await readiness.json()).status, "ready");
} finally {
  child.kill("SIGTERM");
}
