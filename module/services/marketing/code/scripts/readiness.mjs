import { readGeneratedConfig } from "./read-generated-config.mjs";

const config = await readGeneratedConfig();
const strict =
  process.argv.includes("--production") ||
  process.env.MARKETING_STRICT_READINESS === "1";
const reportOnly = process.argv.includes("--report");
const missing = [];

if (config.developmentFixture) missing.push("replace the developmentFixture config");
for (const [name, value] of Object.entries(config.urls)) {
  if (new URL(value).hostname.endsWith("example.com")) {
    missing.push(`replace urls.${name}`);
  }
}
for (const [name, value] of Object.entries(config.contacts)) {
  if (value.endsWith(".invalid")) missing.push(`replace contacts.${name}`);
}
if (config.pricing.visible && !process.env.MARKETING_CATALOG_URL) {
  missing.push("set MARKETING_CATALOG_URL for authoritative public pricing");
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
          pricing: !missing.some((item) => item.includes("CATALOG")),
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
