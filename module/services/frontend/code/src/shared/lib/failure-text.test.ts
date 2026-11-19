import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { failureText } from "./failure-text";

describe("failureText", () => {
	it("describes a failed precondition without exposing server wording", () => {
		const refused = new ConnectError(
			"the team still holds a role",
			Code.FailedPrecondition,
		);
		expect(failureText("delete the team", refused)).toBe(
			"Couldn't delete the team. The current state doesn't allow this change. Refresh the page to review it.",
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
		).toBe("You don't have access to save. Ask your organization administrator for access.");
	});
});

it.each(Object.values(Code).filter((code): code is Code => typeof code === "number"))("never exposes server details for code %s", code => {
  const message = failureText("change access", new ConnectError("private-policy-detail", code));
  expect(message).not.toContain("private-policy-detail");
  if (code === Code.PermissionDenied) {
    expect(message).toContain("administrator");
    expect(message).not.toMatch(/try again/i);
  }
});
