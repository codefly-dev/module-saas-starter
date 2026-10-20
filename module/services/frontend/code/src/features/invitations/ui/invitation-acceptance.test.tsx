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

const searchParams = vi.hoisted(() => ({ current: new URLSearchParams() }));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ replace: vi.fn() }),
	useSearchParams: () => searchParams.current,
}));

import { InvitationAcceptance } from "./invitation-acceptance";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	authState.switchOrganization.mockReset();
	authState.isAuthenticated = true;
	searchParams.current = new URLSearchParams();
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

// The emailed link carries the token in the query; `proxy.ts` moves it into a
// cookie that lives fifteen minutes and redirects to the clean URL. A sign-up,
// an MFA enrolment or a passkey ceremony easily outlasts that, so the return
// path may not depend on the cookie: the first inspect reports the invitation's
// id, and the page sends the person back by it.
describe("a sign-in longer than the emailed link's cookie", () => {
	const invitationId = "00000000-0000-4000-8000-000000000042";
	const summary = {
		id: invitationId,
		status: "INVITATION_STATUS_PENDING",
		emailHint: "j•••@example.com",
	};

	it("returns through the invitation id and still offers acceptance", async () => {
		// The route answers its own 400 once the cookie is gone and no id came
		// with the request — which is what the page must no longer provoke.
		let cookieAlive = true;
		vi.spyOn(globalThis, "fetch").mockImplementation(
			async (_input: unknown, init?: { body?: unknown }) => {
				const body = JSON.parse(String(init?.body)) as {
					invitationId?: string;
				};
				if (!body.invitationId && !cookieAlive) {
					return new Response(
						JSON.stringify({
							error:
								"This invitation link timed out in this browser after fifteen minutes. Open it from the email again.",
						}),
						{ status: 400 },
					);
				}
				return new Response(JSON.stringify(summary), { status: 200 });
			},
		);

		authState.isAuthenticated = false;
		render(<InvitationAcceptance />);

		// The kit's Button keeps the button role when it renders as an anchor.
		const signIn = await screen.findByRole("button", {
			name: /sign in or create account/i,
		});
		expect(signIn.getAttribute("href")).toBe(
			`/auth/login?next=${encodeURIComponent(
				`/invitations/accept?id=${invitationId}`,
			)}`,
		);

		// Sign-in took longer than fifteen minutes, so the browser comes back
		// with the cookie expired and only the id the link above carried.
		cleanup();
		cookieAlive = false;
		authState.isAuthenticated = true;
		searchParams.current = new URLSearchParams(`id=${invitationId}`);
		render(<InvitationAcceptance />);

		expect(
			await screen.findByRole("button", { name: "Accept invitation" }),
		).toBeTruthy();
		expect(screen.queryByText(/timed out in this browser/i)).toBeNull();
	});

	it("names the timed-out link rather than calling the invitation invalid", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(
				JSON.stringify({
					error:
						"This invitation link timed out in this browser after fifteen minutes. Open it from the email again.",
				}),
				{ status: 400 },
			),
		);

		render(<InvitationAcceptance />);

		await screen.findByText(
			"This invitation link timed out in this browser after fifteen minutes. Open it from the email again.",
		);
		expect(screen.queryByText(/invalid, expired, revoked/i)).toBeNull();
	});
});
