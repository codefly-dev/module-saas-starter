import { describe, expect, it, vi } from "vitest";
import {
	SIGN_OUT_COMPLETE_DESTINATION,
	SIGN_OUT_RETRY_DESTINATION,
	signOutAndLeave,
	signOutDestination,
} from "@/lib/sign-out-journey";

// Every user-facing sign-out control shares this decision, so it is asserted
// once here and the controls are asserted to USE it rather than each re-testing
// the whole matrix.
describe("the shared sign-out journey", () => {
	it("sends a completed sign-out to the login page", () => {
		expect(signOutDestination({ complete: true })).toBe(
			SIGN_OUT_COMPLETE_DESTINATION,
		);
	});

	// The three ways a sign-out fails to finish are ONE outcome, because they are
	// one fact about the session: nothing confirmed it ended. An incomplete
	// answer, a rejected call, and a caller that ignored the result entirely all
	// leave an unmarked access token valid until it expires.
	it.each([
		["the server reported incompleteness", async () => ({ complete: false })],
		[
			"the call rejected",
			async () => {
				throw new Error("network");
			},
		],
		["nothing was reported at all", async () => undefined],
	])("sends %s to the retry journey", async (_name, logout) => {
		const navigate = vi.fn();
		await signOutAndLeave(
			logout as unknown as () => Promise<{ complete: boolean }>,
			{ navigate },
		);
		expect(navigate).toHaveBeenCalledWith(SIGN_OUT_RETRY_DESTINATION);
	});

	// A control may choose where a FINISHED sign-out goes — the invitation flow
	// returns to the invitation it was signed into the wrong account for — and
	// may not choose what an unfinished one looks like.
	it("honours a control's own completed destination, never its own retry", async () => {
		const navigate = vi.fn();
		const completeDestination = "/auth/login?next=%2Finvite%2Fabc";

		await signOutAndLeave(async () => ({ complete: true }), {
			completeDestination,
			navigate,
		});
		expect(navigate).toHaveBeenCalledWith(completeDestination);

		navigate.mockClear();
		await signOutAndLeave(async () => ({ complete: false }), {
			completeDestination,
			navigate,
		});
		expect(navigate).toHaveBeenCalledWith(SIGN_OUT_RETRY_DESTINATION);
	});

	// The retry journey is a real page, not a string nobody serves: /auth/logout
	// is what reports the failure and offers the retry, and the refresh cookie
	// the server kept is the credential that retry uses.
	it("names the page that owns the incomplete case", () => {
		expect(SIGN_OUT_RETRY_DESTINATION).toBe("/auth/logout");
	});
});
