import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const toolsDirectory = path.dirname(fileURLToPath(import.meta.url));
const moduleDirectory = path.resolve(toolsDirectory, "..");
const sourcePath = path.join(moduleDirectory, "public", "site.config.json");
const outputPaths = [
  path.join(
    moduleDirectory,
    "services",
    "frontend",
    "code",
    "src",
    "generated",
    "public-site-config.ts",
  ),
  path.join(
    moduleDirectory,
    "services",
    "marketing",
    "code",
    "src",
    "generated",
    "public-site-config.ts",
  ),
];

const HEX_COLOR = /^#[0-9a-f]{6}$/i;
const SAFE_FONT = /^[^{};\r\n]+$/;
const SAFE_PATH = /^\/(?!\/)[a-z0-9/_-]*$/;
const CONTACT = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
const RESERVED_KEY = /(?:secret|password|private.?key|access.?token|api.?key)/i;
const ALLOWED_MODES = new Set([
  "open_signup",
  "waitlist",
  "invite_only",
  "request_demo",
]);
const ALLOWED_ATTRIBUTION = new Set([
  "referral",
  "referrer",
  "utm_campaign",
  "utm_content",
  "utm_medium",
  "utm_source",
  "utm_term",
]);

function assert(condition, message) {
  if (!condition) throw new Error(`Invalid public site configuration: ${message}`);
}

function exactKeys(value, allowed, context) {
  assert(
    value && typeof value === "object" && !Array.isArray(value),
    `${context} must be an object`,
  );
  const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
  assert(unknown.length === 0, `${context} has unknown field '${unknown[0]}'`);
  for (const key of allowed) assert(key in value, `${context}.${key} is required`);
}

function nonEmpty(value, context, maximum = 512) {
  assert(
    typeof value === "string" &&
      value.trim() === value &&
      value.length > 0 &&
      value.length <= maximum,
    `${context} must be a non-empty string of at most ${maximum} characters`,
  );
}

function absoluteURL(value, context) {
  nonEmpty(value, context);
  const parsed = new URL(value);
  assert(
    parsed.protocol === "https:" ||
      (parsed.protocol === "http:" &&
        (parsed.hostname === "localhost" || parsed.hostname.endsWith(".localhost"))),
    `${context} must use HTTPS outside localhost`,
  );
  assert(!parsed.username && !parsed.password, `${context} cannot contain credentials`);
  assert(!parsed.search && !parsed.hash, `${context} cannot contain query or fragment`);
}

function validateNoSecrets(value, pathParts = []) {
  if (!value || typeof value !== "object") return;
  for (const [key, nested] of Object.entries(value)) {
    const field = [...pathParts, key].join(".");
    assert(!RESERVED_KEY.test(key), `${field} is secret-shaped and cannot be public`);
    validateNoSecrets(nested, [...pathParts, key]);
  }
}

export function validatePublicSiteConfig(config) {
  exactKeys(
    config,
    [
      "schemaVersion",
      "developmentFixture",
      "company",
      "brand",
      "urls",
      "socialProfiles",
      "contacts",
      "locales",
      "acquisition",
      "analytics",
      "pricing",
      "content",
      "customers",
      "testimonials",
    ],
    "root",
  );
  assert(config.schemaVersion === 1, "schemaVersion must be 1");
  assert(
    typeof config.developmentFixture === "boolean",
    "developmentFixture must be boolean",
  );
  validateNoSecrets(config);

  exactKeys(
    config.company,
    ["name", "productName", "shortDescription", "longDescription"],
    "company",
  );
  nonEmpty(config.company.name, "company.name", 80);
  nonEmpty(config.company.productName, "company.productName", 80);
  nonEmpty(config.company.shortDescription, "company.shortDescription", 180);
  nonEmpty(config.company.longDescription, "company.longDescription", 600);

  exactKeys(
    config.brand,
    ["mark", "logoAlt", "favicon", "colors", "typography"],
    "brand",
  );
  nonEmpty(config.brand.mark, "brand.mark", 3);
  nonEmpty(config.brand.logoAlt, "brand.logoAlt", 120);
  assert(SAFE_PATH.test(config.brand.favicon), "brand.favicon must be a safe path");
  exactKeys(
    config.brand.colors,
    ["primary", "accent", "background", "foreground"],
    "brand.colors",
  );
  for (const [name, color] of Object.entries(config.brand.colors)) {
    assert(HEX_COLOR.test(color), `brand.colors.${name} must be a six-digit hex color`);
  }
  exactKeys(config.brand.typography, ["sans", "heading"], "brand.typography");
  for (const [name, font] of Object.entries(config.brand.typography)) {
    nonEmpty(font, `brand.typography.${name}`, 180);
    assert(SAFE_FONT.test(font), `brand.typography.${name} is unsafe`);
  }

  exactKeys(config.urls, ["apex", "marketing", "app", "docs", "status"], "urls");
  for (const [name, url] of Object.entries(config.urls)) absoluteURL(url, `urls.${name}`);

  assert(Array.isArray(config.socialProfiles), "socialProfiles must be an array");
  for (const [index, profile] of config.socialProfiles.entries()) {
    exactKeys(profile, ["label", "url"], `socialProfiles[${index}]`);
    nonEmpty(profile.label, `socialProfiles[${index}].label`, 40);
    absoluteURL(profile.url, `socialProfiles[${index}].url`);
  }

  exactKeys(
    config.contacts,
    ["support", "privacy", "security", "legal", "sales"],
    "contacts",
  );
  for (const [name, email] of Object.entries(config.contacts)) {
    assert(CONTACT.test(email), `contacts.${name} must be an email address`);
  }

  exactKeys(config.locales, ["default", "enabled"], "locales");
  assert(
    Array.isArray(config.locales.enabled) && config.locales.enabled.length > 0,
    "locales.enabled must not be empty",
  );
  const canonicalLocales = Intl.getCanonicalLocales(config.locales.enabled);
  assert(
    canonicalLocales.length === config.locales.enabled.length &&
      canonicalLocales.every((locale, index) => locale === config.locales.enabled[index]),
    "locales.enabled must contain unique canonical locale identifiers",
  );
  assert(
    config.locales.enabled.includes(config.locales.default),
    "locales.default must be enabled",
  );

  exactKeys(
    config.acquisition,
    ["mode", "signupPath", "loginPath", "waitlistPath", "allowedAttribution"],
    "acquisition",
  );
  assert(ALLOWED_MODES.has(config.acquisition.mode), "acquisition.mode is unsupported");
  for (const name of ["signupPath", "loginPath"]) {
    assert(SAFE_PATH.test(config.acquisition[name]), `acquisition.${name} is unsafe`);
  }
  assert(
    config.acquisition.waitlistPath === null ||
      SAFE_PATH.test(config.acquisition.waitlistPath),
    "acquisition.waitlistPath is unsafe",
  );
  assert(
    Array.isArray(config.acquisition.allowedAttribution) &&
      config.acquisition.allowedAttribution.every((field) =>
        ALLOWED_ATTRIBUTION.has(field),
      ),
    "acquisition.allowedAttribution contains an unsupported field",
  );
  assert(
    [...config.acquisition.allowedAttribution].sort().join("\n") ===
      config.acquisition.allowedAttribution.join("\n"),
    "acquisition.allowedAttribution must be sorted",
  );
  if (config.acquisition.mode === "waitlist") {
    assert(config.acquisition.waitlistPath, "waitlist mode requires waitlistPath");
  }

  exactKeys(config.analytics, ["adapter", "consentRequired"], "analytics");
  assert(
    config.analytics.adapter === "none",
    "only the consent-safe none analytics adapter ships by default",
  );
  assert(config.analytics.consentRequired === true, "analytics must require consent");
  exactKeys(config.pricing, ["visible", "catalogPath"], "pricing");
  assert(typeof config.pricing.visible === "boolean", "pricing.visible must be boolean");
  assert(SAFE_PATH.test(config.pricing.catalogPath), "pricing.catalogPath is unsafe");
  exactKeys(config.content, ["provider"], "content");
  assert(
    config.content.provider === "repository",
    "the starter content provider must be repository",
  );
  assert(Array.isArray(config.customers), "customers must be an array");
  assert(Array.isArray(config.testimonials), "testimonials must be an array");
  assert(
    config.developmentFixture ||
      (config.customers.length === 0 && config.testimonials.length === 0),
    "customer and testimonial claims require explicit adopter review",
  );
  return config;
}

export function renderPublicSiteConfig(config) {
  validatePublicSiteConfig(config);
  return [
    "// Code generated from module/public/site.config.json. DO NOT EDIT.",
    "",
    `export const publicSiteConfig = Object.freeze(${JSON.stringify(config, null, 2)} as const);`,
    "",
  ].join("\n");
}

async function main() {
  const config = JSON.parse(await readFile(sourcePath, "utf8"));
  const rendered = renderPublicSiteConfig(config);
  const check = process.argv.includes("--check");
  for (const outputPath of outputPaths) {
    if (check) {
      const current = await readFile(outputPath, "utf8").catch(() => "");
      assert(current === rendered, `${path.relative(moduleDirectory, outputPath)} is stale`);
    } else {
      await writeFile(outputPath, rendered);
    }
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
