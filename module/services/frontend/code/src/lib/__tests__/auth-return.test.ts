import { describe, expect, it } from "vitest";
import { safeReturnPath } from "../auth-return";

describe("safeReturnPath", () => {
	it("keeps a relative invitation return path", () => {
		expect(safeReturnPath("/invitations/accept?id=invite-1")).toBe(
			"/invitations/accept?id=invite-1",
		);
	});

	it.each([
		"https://attacker.example",
		"//attacker.example/path",
		"/\\attacker.example",
		"javascript:alert(1)",
	])("rejects an unsafe return target", (target) => {
		expect(safeReturnPath(target)).toBe("/");
	});
});
