import assert from "node:assert/strict";
import test from "node:test";
import {
  approvedAttribution,
  productHandoff,
} from "@/lib/cta";

test("preserves only approved bounded attribution fields", () => {
  const query = approvedAttribution({
    utm_source: "launch",
    utm_campaign: ["summer", "ignored"],
    email: "private@example.com",
    return_url: "https://attacker.example",
    referrer: "x".repeat(300),
  });
  assert.equal(query.get("utm_source"), "launch");
  assert.equal(query.get("utm_campaign"), "summer");
  assert.equal(query.has("email"), false);
  assert.equal(query.has("return_url"), false);
  assert.equal(query.get("referrer")?.length, 200);
});

test("uses only the configured product origin and canonical plan key", () => {
  const handoff = new URL(
    productHandoff("signup", { utm_source: "launch" }, "pro")!,
  );
  assert.equal(handoff.origin, "https://app.example.com");
  assert.equal(handoff.pathname, "/auth/login");
  assert.equal(handoff.searchParams.get("utm_source"), "launch");
  assert.equal(handoff.searchParams.get("plan"), "pro");

  const invalidPlan = new URL(
    productHandoff("signup", {}, "../../enterprise")!,
  );
  assert.equal(invalidPlan.searchParams.has("plan"), false);
});
