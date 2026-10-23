import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { isMFARequired, stepUpHref } from "./step-up";

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
