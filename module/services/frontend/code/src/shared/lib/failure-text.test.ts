import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { failureText } from "./failure-text";

describe("failureText", () => {
	it("passes on a refusal the host worded", () => {
		const refused = new ConnectError(
			"the team still holds a role",
			Code.FailedPrecondition,
		);
		expect(failureText("delete the team", refused)).toBe(
			"Couldn't delete the team: the team still holds a role",
		);
	});

	it("never shows a transport or internal error's own text", () => {
		for (const error of [
			new TypeError("Failed to fetch"),
			new ConnectError("pq: connection refused", Code.Internal),
			new ConnectError("upstream unavailable", Code.Unavailable),
			"boom",
		]) {
			expect(failureText("save the team", error)).toBe(
				"Couldn't save the team. Try again; if it keeps failing, tell your administrator.",
			);
		}
	});

	it("says it plainly when a refusal carries no words", () => {
		expect(
			failureText("save", new ConnectError("", Code.PermissionDenied)),
		).toMatch(/^Couldn't save\. Try again/);
	});
});
