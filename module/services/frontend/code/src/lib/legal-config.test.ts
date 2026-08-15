import { afterEach, describe, expect, it, vi } from "vitest";
import { legalContentConfigured } from "./legal-config";

const legalKeys = [
	"NEXT_PUBLIC_IDENTITY_PROVIDER",
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
	it("fills unconfigured fixture/dev stacks with placeholder legal content", async () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "fixture";
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(true);
	});

	it("treats an unset identity provider as fixture and supplies placeholders", async () => {
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(true);
	});

	it("leaves a real provider unconfigured without operator content", async () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "workos";
		const config = await loadConfig();
		expect(legalContentConfigured(config)).toBe(false);
	});

	it("uses operator content verbatim when supplied", async () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "fixture";
		process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME = "Acme";
		process.env.NEXT_PUBLIC_LEGAL_CONTACT_EMAIL = "legal@acme.test";
		process.env.NEXT_PUBLIC_LEGAL_TERMS_CONTENT = "Acme terms";
		process.env.NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT = "Acme privacy";
		const config = await loadConfig();
		expect(config.entityName).toBe("Acme");
		expect(config.termsContent).toBe("Acme terms");
	});
});
