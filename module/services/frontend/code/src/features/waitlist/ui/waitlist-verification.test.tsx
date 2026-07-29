import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WaitlistVerification } from "./waitlist-verification";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

describe("WaitlistVerification", () => {
	it("renders an error state for an expired verification token", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(
				JSON.stringify({
					state: "WAITLIST_STATE_PENDING",
					message: "This verification link expired.",
				}),
				{ status: 200 },
			),
		);

		render(<WaitlistVerification />);

		await waitFor(() =>
			expect(screen.getByLabelText("Verification failed")).toBeTruthy(),
		);
		expect(screen.getByText("This verification link expired.")).toBeTruthy();
	});

	it("renders success only for a verified state", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(
				JSON.stringify({
					state: "WAITLIST_STATE_VERIFIED",
					message: "Your email is verified.",
				}),
				{ status: 200 },
			),
		);

		render(<WaitlistVerification />);

		await waitFor(() =>
			expect(screen.getByLabelText("Verification succeeded")).toBeTruthy(),
		);
	});
});
