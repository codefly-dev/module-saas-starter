import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { isMFARequired, refusalMessage, stepUpHref } from "./step-up";

describe("isMFARequired", () => {
	it("recognises the host's refusal as a Connect error", () => {
		expect(
			isMFARequired(new ConnectError("mfa_required", Code.FailedPrecondition)),
		).toBe(true);
		expect(
			isMFARequired(new ConnectError("mfa_required", Code.PermissionDenied)),
		).toBe(false);
		expect(
			isMFARequired(new ConnectError("quota", Code.FailedPrecondition)),
		).toBe(false);
	});

	it("recognises the refusal once it has been flattened to a message", () => {
		expect(
			isMFARequired(
				new Error(
					"[failed_precondition] rpc error: code = FailedPrecondition desc = mfa_required",
				),
			),
		).toBe(true);
		expect(isMFARequired(new Error("mfa_required"))).toBe(false);
		expect(isMFARequired(new Error("network down"))).toBe(false);
	});
});

describe("stepUpHref", () => {
	it("sends the person to sign in and back to where they were", () => {
		expect(stepUpHref("/admin/platform/admins?tab=1")).toBe(
			"/auth/login?step_up=1&next=%2Fadmin%2Fplatform%2Fadmins%3Ftab%3D1",
		);
	});
});

describe("refusalMessage", () => {
	it("shows the server's own words when the organization would lose its last administrator", () => {
		expect(
			refusalMessage(
				new ConnectError(
					"organization must keep at least one owner or admin",
					Code.FailedPrecondition,
				),
				"Failed to remove member",
			),
		).toBe("organization must keep at least one owner or admin");
	});

	// The host sends the quota's own sentence, never the wrapped call path, so
	// the person learns which limit they hit.
	it("shows the quota the request hit", () => {
		expect(
			refusalMessage(
				new ConnectError(
					"seats quota exhausted: used 5 of 5",
					Code.ResourceExhausted,
				),
				"Failed to add member",
			),
		).toBe("seats quota exhausted: used 5 of 5");
	});

	it("does not put server internals in front of the user", () => {
		expect(
			refusalMessage(
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
			refusalMessage(new Error("network down"), "Failed to add member"),
		).toBe("Failed to add member");
	});

	it("falls back when the server sent no message to show", () => {
		expect(
			refusalMessage(
				new ConnectError("", Code.FailedPrecondition),
				"Failed to remove member",
			),
		).toBe("Failed to remove member");
	});
});
