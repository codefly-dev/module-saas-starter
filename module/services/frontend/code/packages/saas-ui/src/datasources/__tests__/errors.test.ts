import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { messageOf } from "../errors.js";

describe("source error presentation", () => {
	it.each([
		new Error("credential=SECRET"),
		new ConnectError("credential=SECRET", Code.Internal),
		new ConnectError("credential=SECRET", Code.InvalidArgument),
	])("never echoes diagnostics", (error) => {
		expect(messageOf(error)).not.toContain("SECRET");
		expect(messageOf(error)).not.toContain("credential=");
	});
	it("directs denied access to an administrator without a retry", () => {
		const message = messageOf(
			new ConnectError("role_missing", Code.PermissionDenied),
		);
		expect(message).toContain("administrator");
		expect(message).not.toMatch(/try again|retry/i);
	});
});
