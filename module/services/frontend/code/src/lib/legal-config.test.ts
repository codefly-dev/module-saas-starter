import { describe, expect, it } from "vitest";
import { legalContentConfigured } from "./legal-config";

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
