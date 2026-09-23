import assert from "node:assert/strict";
import test from "node:test";
import { GET as health } from "../src/app/api/health/route";
import { GET as healthz, dynamic } from "../src/app/api/healthz/route";

test("the deployment probe uses the existing dependency-free health handler", async () => {
  assert.equal(healthz, health);
  assert.equal(dynamic, "force-dynamic");
  const response = healthz();
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  const payload = await response.json();
  assert.equal(payload.status, "ok");
  assert.equal(payload.service, "marketing");
});
