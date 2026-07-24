import { describe, expect, it } from "vitest";
import { profileInitials, stringProfilePatch } from "./profile";

describe("user profile model", () => {
	it("keeps only string values when translating a typed patch", () => {
		expect(
			stringProfilePatch({
				name: "Ada Lovelace",
				display_name: "ada",
				title: undefined,
			}),
		).toEqual({
			name: "Ada Lovelace",
			display_name: "ada",
		});
	});

	it("renders stable initials from a profile label", () => {
		expect(profileInitials("Ada Lovelace")).toBe("AL");
		expect(profileInitials("Ada")).toBe("AD");
		expect(profileInitials("")).toBe("U");
	});

	it("keeps blank values in the patch so the server clears those keys", () => {
		expect(
			stringProfilePatch({ name: "Ada Lovelace", title: "" }),
		).toEqual({ name: "Ada Lovelace", title: "" });
	});
});
