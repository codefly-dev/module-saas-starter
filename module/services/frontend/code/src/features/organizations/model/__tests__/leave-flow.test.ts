import { describe, expect, it } from "vitest";
import { fallbackOrganization, isPersonalOrganization } from "../leave-flow";

describe("fallbackOrganization", () => {
	it("picks another organization than the one being left", () => {
		const organizations = [
			{ id: "a", name: "Alpha" },
			{ id: "b", name: "Beta" },
		];
		expect(fallbackOrganization(organizations, "a")?.id).toBe("b");
	});

	it("has nowhere to go from the only organization", () => {
		expect(fallbackOrganization([{ id: "a", name: "Alpha" }], "a")).toBe(
			undefined,
		);
	});
});

describe("isPersonalOrganization", () => {
	it("recognizes the organization registration created", () => {
		expect(
			isPersonalOrganization({ name: "Personal", slug: "personal-1a2b3c" }),
		).toBe(true);
	});

	it("treats a renamed personal organization as a workspace", () => {
		expect(
			isPersonalOrganization({ name: "Acme", slug: "personal-1a2b3c" }),
		).toBe(false);
	});
});
