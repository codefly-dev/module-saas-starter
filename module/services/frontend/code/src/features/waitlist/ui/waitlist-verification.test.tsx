import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
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

		await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
		expect(screen.getByText(/Request a new link to continue/)).toBeTruthy();
		expect(screen.queryByText("This verification link expired.")).toBeNull();
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

it("offers retry for a temporary failure without showing the server message", async () => {
	const fetch = vi
		.spyOn(globalThis, "fetch")
		.mockResolvedValueOnce(
			Response.json({ message: "private diagnostics" }, { status: 503 }),
		)
		.mockResolvedValueOnce(Response.json({ state: "WAITLIST_STATE_VERIFIED" }));
	render(<WaitlistVerification />);
	fireEvent.click(await screen.findByRole("button", { name: "Try again" }));
	await screen.findByLabelText("Verification succeeded");
	expect(fetch).toHaveBeenCalledTimes(2);
	expect(screen.queryByText("private diagnostics")).toBeNull();
});
