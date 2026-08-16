import { afterEach, describe, expect, it, vi } from "vitest";
import { legalContentConfigured } from "./legal-config";

const legalKeys = [
	"NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER",
	"NEXT_PUBLIC_LEGAL_ENTITY_NAME",
	"NEXT_PUBLIC_LEGAL_CONTACT_EMAIL",
	"NEXT_PUBLIC_LEGAL_TERMS_CONTENT",
	"NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT",
] as const;

afterEach(() => {
	for (const key of legalKeys) delete process.env[key];
	vi.resetModules();
});

async function loadConfig() {
	return (await import("./legal-config")).legalContentConfig;
}

describe("legalContentConfigured", () => {
	it("does not treat operator labels as production legal content", () => {
		expect(
			legalContentConfigured({
				entityName: "Acme",
				contactEmail: "legal@example.com",
			}),
		).toBe(false);
	});

	it("requires both operator-provided legal documents", () => {
		expect(
			legalContentConfigured({
				entityName: "Acme",
				contactEmail: "legal@example.com",
				termsContent: "Operator terms",
				privacyContent: "Operator privacy policy",
			}),
		).toBe(true);
	});
});

describe("legalContentConfig", () => {
	it("fills unconfigured dev stacks with placeholders when the dev flag is set", async () => {
		process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER = "true";
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(true);
	});

	it("keeps the terms gate closed by default when the dev flag is absent", async () => {
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(false);
	});

	it('accepts "1" as the dev-placeholder flag', async () => {
		process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER = "1";
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(true);
	});

	it("uses operator content verbatim without the dev flag", async () => {
		process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME = "Acme";
		process.env.NEXT_PUBLIC_LEGAL_CONTACT_EMAIL = "legal@acme.test";
		process.env.NEXT_PUBLIC_LEGAL_TERMS_CONTENT = "Acme terms";
		process.env.NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT = "Acme privacy";
		const config = await loadConfig();
		expect(config.entityName).toBe("Acme");
		expect(config.termsContent).toBe("Acme terms");
		expect(legalContentConfigured(config)).toBe(true);
	});

	it("keeps partial operator content and fills only the gaps under the dev flag", async () => {
		process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER = "true";
		process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME = "Acme";
		const config = await loadConfig();
		expect(config.entityName).toBe("Acme");
		expect(config.contactEmail).toBe("dev@localhost");
		expect(config.termsContent).toContain("local development only");
		expect(config.privacyContent).toContain("local development only");
		expect(legalContentConfigured(config)).toBe(true);
	});
});
