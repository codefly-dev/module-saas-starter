import { describe, expect, it } from "vitest";
import {
	ADMIN_CARD_DESCRIPTIONS,
	ADMIN_OVERVIEW_IDS,
} from "./admin-overview-descriptions";

describe("Admin overview cards", () => {
	// Every card said "Open <label>." — the label again. Each card the catalog
	// can put on the overview now says what is behind it.
	it("describe every card the catalog offers, in words other than its label", () => {
		expect(ADMIN_OVERVIEW_IDS.length).toBeGreaterThan(10);
		for (const id of ADMIN_OVERVIEW_IDS) {
			const description = ADMIN_CARD_DESCRIPTIONS[id];
			expect(description, id).toBeTruthy();
			expect(description, id).not.toMatch(/^Open /);
		}
	});

	it("carry no description for an entry the catalog no longer has", () => {
		for (const id of Object.keys(ADMIN_CARD_DESCRIPTIONS)) {
			expect(ADMIN_OVERVIEW_IDS, id).toContain(id);
		}
	});
});
