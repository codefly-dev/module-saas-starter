import { Code, ConnectError } from "@connectrpc/connect";
import { expect, test } from "vitest";
import { lifecycleErrorMessage } from "../errors";

test.each([
	Code.PermissionDenied,
	Code.Unauthenticated,
	Code.InvalidArgument,
	Code.AlreadyExists,
	Code.NotFound,
	Code.ResourceExhausted,
	Code.FailedPrecondition,
	Code.Internal,
])("does not disclose server diagnostics for %s", (code) => {
	const message = lifecycleErrorMessage(
		new ConnectError("internal permission name and server details", code),
		"Couldn't save the organization.",
	);
	expect(message).not.toContain("internal permission");
	if (code === Code.PermissionDenied) {
		expect(message).toContain("organization administrator");
		expect(message).not.toContain("Try again");
	}
});
