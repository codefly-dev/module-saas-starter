import assert from "node:assert/strict";
import test from "node:test";

import {
  renderPublicSiteConfig,
  validatePublicSiteConfig,
} from "./generate-public-config.mjs";

const valid = {
  schemaVersion: 1,
  developmentFixture: false,
  company: {
    name: "Acme",
    productName: "Orbit",
    shortDescription: "Ship reliable software.",
    longDescription: "A complete description suitable for public metadata.",
  },
  brand: {
    mark: "O",
    logoAlt: "Orbit",
    favicon: "/icon",
    colors: {
      primary: "#112233",
      accent: "#445566",
      background: "#ffffff",
      foreground: "#000000",
    },
    typography: { sans: "Arial, sans-serif", heading: "Arial, sans-serif" },
  },
  urls: {
    apex: "https://acme.test",
    marketing: "https://www.acme.test",
    app: "https://app.acme.test",
    docs: "https://docs.acme.test",
    status: "https://status.acme.test",
  },
  socialProfiles: [],
  contacts: {
    support: "support@acme.test",
    privacy: "privacy@acme.test",
    security: "security@acme.test",
    legal: "legal@acme.test",
    sales: "sales@acme.test",
  },
  locales: { default: "en", enabled: ["en"] },
  acquisition: {
    mode: "open_signup",
    signupPath: "/auth/login",
    loginPath: "/auth/login",
    waitlistPath: null,
    allowedAttribution: ["utm_campaign", "utm_source"],
  },
  analytics: { adapter: "none", consentRequired: true },
  pricing: { visible: true, catalogPath: "/v1/public/plans" },
  content: { provider: "repository" },
  customers: [],
  testimonials: [],
};

test("accepts a complete public-only configuration deterministically", () => {
  assert.equal(validatePublicSiteConfig(structuredClone(valid)).company.name, "Acme");
  assert.equal(
    renderPublicSiteConfig(structuredClone(valid)),
    renderPublicSiteConfig(structuredClone(valid)),
  );
});

test("rejects secret-shaped fields and unsafe handoff URLs", () => {
  const secret = structuredClone(valid);
  secret.analytics.apiKey = "do-not-serialize";
  assert.throws(() => validatePublicSiteConfig(secret), /unknown field|secret-shaped/);

  const redirect = structuredClone(valid);
  redirect.acquisition.signupPath = "https://attacker.example";
  assert.throws(() => validatePublicSiteConfig(redirect), /signupPath is unsafe/);
});

test("accepts reviewed production claims and rejects them in fixtures", () => {
  const claims = structuredClone(valid);
  claims.customers.push({
    name: "Customer",
    logo: "/customers/customer.svg",
    url: "https://customer.test",
  });
  claims.testimonials.push({
    quote: "The configured product works for us.",
    attribution: "A. Customer",
    role: "Product lead",
  });
  assert.equal(validatePublicSiteConfig(claims).customers.length, 1);

  claims.developmentFixture = true;
  assert.throws(
    () => validatePublicSiteConfig(claims),
    /development fixtures cannot include/,
  );
});
