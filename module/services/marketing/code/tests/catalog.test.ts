import assert from "node:assert/strict";
import test from "node:test";
import { formatPlanAmount, parsePublicCatalog } from "@/lib/catalog";

const plan = {
  key: "pro",
  name: "Pro",
  description: "For growing product teams.",
  currency: "EUR",
  amountMinor: 4900,
  interval: "month" as const,
  checkoutEnabled: true,
  contactSales: false,
  trialDays: 14,
  taxBehavior: "automatic" as const,
  fixture: false,
  entitlements: [{ key: "seats", limit: 50 }],
};

test("accepts the sanitized public catalog and formats minor units", () => {
  const catalog = parsePublicCatalog({ revision: "catalog-v1", plans: [plan] });
  assert.equal(catalog.plans[0].key, "pro");
  assert.match(formatPlanAmount(plan, "en"), /49/);
});

test("rejects checkout secrets and unsupported intervals", () => {
  assert.throws(
    () =>
      parsePublicCatalog({
        revision: "catalog-v1",
        plans: [{ ...plan, stripePriceId: "price_secret" }],
      }),
    /unrecognized key/i,
  );
  assert.throws(
    () =>
      parsePublicCatalog({
        revision: "catalog-v1",
        plans: [{ ...plan, interval: "quarter" }],
      }),
    /invalid enum/i,
  );
});
