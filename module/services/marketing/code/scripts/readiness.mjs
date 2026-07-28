import { readGeneratedConfig } from "./read-generated-config.mjs";

const config = await readGeneratedConfig();
const strict =
  process.argv.includes("--production") ||
  process.env.MARKETING_STRICT_READINESS === "1";
const reportOnly = process.argv.includes("--report");
const missing = [];
let pricingReady = !config.pricing.visible;

if (config.developmentFixture) missing.push("replace the developmentFixture config");
for (const [name, value] of Object.entries(config.urls)) {
  if (new URL(value).hostname.endsWith("example.com")) {
    missing.push(`replace urls.${name}`);
  }
}
for (const [name, value] of Object.entries(config.contacts)) {
  if (value.endsWith(".invalid")) missing.push(`replace contacts.${name}`);
}
if (config.pricing.visible) {
  const catalogURL = process.env.MARKETING_CATALOG_URL;
  if (!catalogURL) {
    missing.push("set MARKETING_CATALOG_URL for authoritative public pricing");
  } else {
    try {
      const response = await fetch(
        new URL(config.pricing.catalogPath, catalogURL),
        {
          credentials: "omit",
          headers: { Accept: "application/json" },
          signal: AbortSignal.timeout(2000),
        },
      );
      if (!response.ok) {
        missing.push(
          `make the public pricing catalog return HTTP 200 (received ${response.status})`,
        );
      } else {
        const catalog = await response.json();
        const plans = Array.isArray(catalog?.plans) ? catalog.plans : null;
        if (!plans || plans.length === 0) {
          missing.push("publish at least one authoritative public pricing plan");
        } else if (
          plans.some(
            (plan) =>
              !plan ||
              typeof plan.key !== "string" ||
              typeof plan.currency !== "string" ||
              typeof plan.amountMinor !== "number" ||
              typeof plan.checkoutEnabled !== "boolean" ||
              plan.fixture === true,
          )
        ) {
          missing.push(
            "replace fixture or invalid entries in the public pricing catalog",
          );
        } else {
          pricingReady = true;
        }
      }
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      missing.push(`make the public pricing catalog reachable (${detail})`);
    }
  }
}
if (process.env.MARKETING_ENABLED === "false") {
  missing.push("enable the marketing service for a public launch");
}
if (process.env.MARKETING_INDEXABLE !== "true") {
  missing.push("set MARKETING_INDEXABLE=true on the canonical production host");
}

if (reportOnly || strict) {
  const status = missing.length === 0 ? "ready" : "not-ready";
  process.stdout.write(
    `${JSON.stringify(
      {
        service: "marketing",
        status,
        checks: {
          brand: !config.developmentFixture,
          domains: !missing.some((item) => item.startsWith("replace urls.")),
          contacts: !missing.some((item) => item.startsWith("replace contacts.")),
          pricing: pricingReady,
          enabled: process.env.MARKETING_ENABLED !== "false",
          indexable: process.env.MARKETING_INDEXABLE === "true",
        },
        requiredActions: missing,
      },
      null,
      2,
    )}\n`,
  );
}

if (strict && missing.length > 0) {
  process.stderr.write(
    `Marketing production readiness failed:\n${missing
      .map((item) => `- ${item}`)
      .join("\n")}\n`,
  );
  process.exitCode = 1;
}
