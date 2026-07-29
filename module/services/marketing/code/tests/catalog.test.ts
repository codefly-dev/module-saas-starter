import assert from "node:assert/strict";
import test from "node:test";
import {
  catalogDisplayState,
  formatPlanAmount,
  parsePublicCatalog,
} from "@/lib/catalog";

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

test("formats amounts with the currency's ISO minor-unit precision", () => {
  assert.equal(
    formatPlanAmount({ ...plan, currency: "JPY", amountMinor: 4900 }, "en"),
    new Intl.NumberFormat("en", {
      style: "currency",
      currency: "JPY",
    }).format(4900),
  );
  assert.equal(
    formatPlanAmount({ ...plan, currency: "KWD", amountMinor: 4900 }, "en"),
    new Intl.NumberFormat("en", {
      style: "currency",
      currency: "KWD",
    }).format(4.9),
  );
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

test("represents an available catalog with no public plans as an empty state", () => {
  assert.deepEqual(
    catalogDisplayState({
      kind: "available",
      catalog: { revision: "catalog-empty", plans: [] },
    }),
    { kind: "empty", revision: "catalog-empty" },
  );
});
