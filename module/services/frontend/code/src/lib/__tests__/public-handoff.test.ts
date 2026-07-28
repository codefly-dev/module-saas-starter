import { describe, expect, it } from "vitest";
import {
	publicHandoffDestination,
	safePostLoginDestination,
} from "../public-handoff";

describe("public product handoff", () => {
	it("carries the selected plan and approved attribution into onboarding", () => {
		const query = new URLSearchParams({
			plan: "pro",
			utm_source: "launch",
			utm_campaign: "summer",
			email: "private@example.com",
			return_url: "https://attacker.example",
		});

		expect(publicHandoffDestination(query)).toBe(
			"/onboarding?plan=pro&utm_campaign=summer&utm_source=launch",
		);
	});

	it("prefers a safe product return path and rejects redirect targets", () => {
		expect(
			publicHandoffDestination(
				new URLSearchParams({ next: "/admin/billing?tab=plans" }),
			),
		).toBe("/admin/billing?tab=plans");
		expect(safePostLoginDestination("//attacker.example/path")).toBe("/");
		expect(
			safePostLoginDestination("https://attacker.example/path"),
		).toBe("/");
		expect(safePostLoginDestination("/auth/login?next=/admin")).toBe("/");
	});
});
