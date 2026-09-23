import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("next/server", () => ({ connection: vi.fn(async () => undefined) }));

import { legalContentConfigured } from "./legal-config";
import { readLegalContent } from "./legal-content";

const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__LEGAL__";
const keys = [
	"NEXT_PUBLIC_LEGAL_ENTITY_NAME",
	"NEXT_PUBLIC_LEGAL_CONTACT_EMAIL",
	"NEXT_PUBLIC_LEGAL_TERMS_CONTENT",
	"NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT",
];

afterEach(() => {
	for (const key of keys) {
		delete process.env[`${prefix}${key}`];
		delete process.env[key];
	}
});

describe("readLegalContent", () => {
	it("reads the legal group from the running process", async () => {
		process.env[`${prefix}NEXT_PUBLIC_LEGAL_ENTITY_NAME`] = "Acme";
		process.env[`${prefix}NEXT_PUBLIC_LEGAL_CONTACT_EMAIL`] = "legal@acme.test";
		process.env[`${prefix}NEXT_PUBLIC_LEGAL_TERMS_CONTENT`] = "Acme terms";
		process.env[`${prefix}NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT`] = "Acme privacy";

		const legal = await readLegalContent();
		expect(legal.entityName).toBe("Acme");
		expect(legal.termsContent).toBe("Acme terms");
		expect(legalContentConfigured(legal)).toBe(true);
	});

	it("keeps the gate closed when the group is empty", async () => {
		expect(legalContentConfigured(await readLegalContent())).toBe(false);
	});
});
