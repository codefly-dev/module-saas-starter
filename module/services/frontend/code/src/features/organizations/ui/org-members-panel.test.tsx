import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { memberErrorMessage } from "./org-members-panel";

describe("memberErrorMessage", () => {
	it("shows the server's own words when the organization would lose its last administrator", () => {
		expect(
			memberErrorMessage(
				new ConnectError(
					"organization must keep at least one owner or admin",
					Code.FailedPrecondition,
				),
				"Failed to remove member",
			),
		).toBe("organization must keep at least one owner or admin");
	});

	// The quota mapping returns the whole wrapped chain, so reporting its text
	// would put the internal call path in a toast. Only FailedPrecondition
	// carries a message written for whoever is reading it.
	it("does not put the internal call path in front of the user on a quota rejection", () => {
		expect(
			memberErrorMessage(
				new ConnectError(
					"AddOrgMember: cannot add member: entitlement quota exceeded",
					Code.ResourceExhausted,
				),
				"Failed to add member",
			),
		).toBe("Failed to add member");
	});

	it("does not put server internals in front of the user", () => {
		expect(
			memberErrorMessage(
				new ConnectError(
					'cannot remove member: pq: connection refused "10.0.0.4:5432"',
					Code.Internal,
				),
				"Failed to remove member",
			),
		).toBe("Failed to remove member");
	});

	it("falls back for an error that never reached the server", () => {
		expect(
			memberErrorMessage(new Error("network down"), "Failed to add member"),
		).toBe("Failed to add member");
	});

	it("falls back when the server sent no message to show", () => {
		expect(
			memberErrorMessage(
				new ConnectError("", Code.FailedPrecondition),
				"Failed to remove member",
			),
		).toBe("Failed to remove member");
	});
});
