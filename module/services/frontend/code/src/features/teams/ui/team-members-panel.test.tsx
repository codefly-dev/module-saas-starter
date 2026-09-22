import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { addMemberErrorMessage } from "./team-members-panel";

describe("addMemberErrorMessage", () => {
	it("shows the server's own words when the target is ineligible", () => {
		expect(
			addMemberErrorMessage(
				new ConnectError(
					"user is not a member of the team's organization",
					Code.FailedPrecondition,
				),
			),
		).toBe("user is not a member of the team's organization");
	});

	it("does not put server internals in front of the user", () => {
		expect(
			addMemberErrorMessage(
				new ConnectError(
					'failed to add team member: pq: connection refused "10.0.0.4:5432"',
					Code.Internal,
				),
			),
		).toBe("Failed to add member");
	});

	it("falls back for an error that never reached the server", () => {
		expect(addMemberErrorMessage(new Error("network down"))).toBe(
			"Failed to add member",
		);
	});
});
