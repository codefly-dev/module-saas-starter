import { describe, expect, it } from "vitest";
import { saveConsequences, saveQuestion } from "./save-question";

const none = { approved: [], revoked: [], changed: [] };

describe("saveQuestion", () => {
	it("names the one module a save approves or revokes", () => {
		expect(saveQuestion({ ...none, approved: ["reports"] })).toBe(
			"Approve the module reports?",
		);
		expect(saveQuestion({ ...none, revoked: ["reports"] })).toBe(
			"Revoke approval for the module reports?",
		);
	});

	it("names every module a wider save touches", () => {
		expect(
			saveQuestion({
				approved: ["search"],
				revoked: ["documents"],
				changed: ["reports"],
			}),
		).toBe("Save permissions for search, documents and reports?");
		expect(saveQuestion({ ...none, changed: ["reports"] })).toBe(
			"Save permissions for reports?",
		);
	});

	it("asks plainly when no module is touched", () => {
		expect(saveQuestion(none)).toBe("Save the module permissions?");
	});
});

describe("saveConsequences", () => {
	it("says what changes for each module and who it reaches", () => {
		const said = saveConsequences({
			approved: ["search"],
			revoked: ["documents"],
			changed: ["reports"],
		});
		expect(said).toContain(
			"search will be able to do what you approved, for every organization on this platform.",
		);
		expect(said).toContain(
			"documents will stop working for every organization within moments",
		);
		expect(said).toContain("What reports may do changes");
	});

	it("says so when nothing is gained or lost", () => {
		expect(saveConsequences(none)).toContain(
			"No module gains or loses an approval with this save.",
		);
	});
});
