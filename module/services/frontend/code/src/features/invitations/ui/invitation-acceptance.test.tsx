import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
	isAuthenticated: true,
	isLoading: false,
	getToken: vi.fn(() => "access-token"),
	switchOrganization: vi.fn(),
	logout: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ replace: vi.fn() }),
	useSearchParams: () => new URLSearchParams(),
}));

import { InvitationAcceptance } from "./invitation-acceptance";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	authState.switchOrganization.mockReset();
});

describe("InvitationAcceptance", () => {
	it("does not offer acceptance for an expired invitation", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(
				JSON.stringify({ status: "INVITATION_STATUS_EXPIRED" }),
				{ status: 200 },
			),
		);

		render(<InvitationAcceptance />);

		await screen.findByText(
			"This invitation expired. Ask the inviter for a new one.",
		);
		expect(
			screen.queryByRole("button", { name: "Accept invitation" }),
		).toBeNull();
	});

	it("shows committed acceptance when workspace switching fails", async () => {
		const request = vi
			.spyOn(globalThis, "fetch")
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({ status: "INVITATION_STATUS_PENDING" }),
					{ status: 200 },
				),
			)
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						organization: { id: "org-1", name: "Acme" },
					}),
					{ status: 200 },
				),
			);
		authState.switchOrganization.mockRejectedValue(
			new Error("refresh unavailable"),
		);

		render(<InvitationAcceptance />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Accept invitation" }),
		);

		await waitFor(() =>
			expect(
				screen.getByText(/invitation was accepted, but this browser/i),
			).toBeTruthy(),
		);
		expect(
			screen.getByRole("button", { name: "Retry workspace switch" }),
		).toBeTruthy();
		expect(request).toHaveBeenCalledTimes(2);
	});
});
