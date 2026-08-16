import assert from "node:assert/strict";
import test from "node:test";
import { renderToReadableStream } from "react-dom/server";
import {
  authorSlug,
  localizedPath,
  renderMarketingPage,
  resolveAuthorBySlug,
} from "@/lib/page-renderer";

test("localized routes keep the default unprefixed and prefix other locales", () => {
  assert.equal(localizedPath(["docs", "guide"], "en", "en"), "/docs/guide");
  assert.equal(localizedPath(["docs", "guide"], "fr", "en"), "/fr/docs/guide");
});

test("author routes use the same canonical slug for generation and lookup", () => {
  assert.equal(authorSlug("Jean-Luc Picard"), "jean-luc-picard");
  assert.equal(authorSlug("  Grace Hopper  "), "grace-hopper");
  assert.equal(
    resolveAuthorBySlug(
      ["Grace Hopper", "Jean-Luc Picard"],
      "jean-luc-picard",
    ),
    "Jean-Luc Picard",
  );
});

test("pricing omits billing intervals from contact-sales labels", async (context) => {
  const originalCatalogURL = process.env.MARKETING_CATALOG_URL;
  process.env.MARKETING_CATALOG_URL = "https://api.example.test";
  context.after(() => {
    if (originalCatalogURL === undefined) {
      delete process.env.MARKETING_CATALOG_URL;
    } else {
      process.env.MARKETING_CATALOG_URL = originalCatalogURL;
    }
  });
  context.mock.method(globalThis, "fetch", async () =>
    Response.json({
      revision: "catalog-v1",
      plans: [
        {
          key: "cloud",
          name: "Cloud",
          description: "For hosted workloads.",
          currency: "USD",
          amountMinor: 3900,
          interval: "month",
          checkoutEnabled: false,
          contactSales: false,
          trialDays: 0,
          taxBehavior: "automatic",
          fixture: false,
          entitlements: [],
        },
        {
          key: "free",
          name: "Free",
          description: "For trying the product.",
          currency: "USD",
          amountMinor: 0,
          interval: "month",
          checkoutEnabled: false,
          contactSales: false,
          trialDays: 0,
          taxBehavior: "automatic",
          fixture: false,
          entitlements: [],
        },
      ],
    }),
  );

  const stream = await renderToReadableStream(
    await renderMarketingPage({ segments: ["pricing"] }),
  );
  await stream.allReady;
  const markup = await new Response(stream).text();

  assert.match(markup, /<p class="price">Contact sales<\/p>/);
  assert.match(markup, /<p class="price">Free<\/p>/);
  assert.doesNotMatch(markup, / \/ month/);
});
