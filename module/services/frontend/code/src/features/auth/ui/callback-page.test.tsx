import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthError } from "@/lib/auth-errors";

const h = vi.hoisted(() => ({
	params: new URLSearchParams(),
	completeOAuth: vi.fn(),
}));

// A fresh function each render, as the real completeOAuth becomes once sign-in
// stores its tokens.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		completeOAuth: (code: string, state: string) =>
			h.completeOAuth(code, state),
	}),
}));

vi.mock("next/navigation", () => ({
	useSearchParams: () => h.params,
}));

import { CallbackPage } from "./callback-page";

beforeEach(() => {
	h.params = new URLSearchParams({ code: "the-code", state: "the-state" });
	h.completeOAuth.mockReset();
	h.completeOAuth.mockReturnValue(new Promise(() => {}));
});

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

describe("CallbackPage", () => {
	it("redeems the authorization code once even when completeOAuth changes identity", () => {
		const { rerender } = render(<CallbackPage />);
		rerender(<CallbackPage />);
		rerender(<CallbackPage />);

		expect(h.completeOAuth).toHaveBeenCalledTimes(1);
		expect(h.completeOAuth).toHaveBeenCalledWith("the-code", "the-state");
	});

	it("renders friendly copy and a support reference without an internal error code", async () => {
		h.completeOAuth.mockRejectedValue(
			new AuthError(
				"We couldn't verify your sign-in. Please start over and try again.",
				{ status: 401, code: "unauthenticated", requestId: "req-abc123" },
			),
		);

		render(<CallbackPage />);

		await waitFor(() =>
			expect(screen.getByText(/verify your sign-in/)).toBeTruthy(),
		);
		// The operator-facing correlation handle is shown so an opaque credential
		// rejection can be tied back to the server log with the real cause.
		expect(screen.getByText(/req-abc123/)).toBeTruthy();
	});

	it("says a refused state check plainly, without the guard's own text or a reference line", async () => {
		vi.spyOn(console, "error").mockImplementation(() => {});
		h.completeOAuth.mockRejectedValue(
			new Error("OAuth state mismatch — possible CSRF attack"),
		);

		render(<CallbackPage />);

		await waitFor(() =>
			expect(
				screen.getByText(/could not be completed, and nothing was changed/),
			).toBeTruthy(),
		);
		expect(screen.queryByText(/CSRF/)).toBeNull();
		expect(screen.queryByText(/Reference:/)).toBeNull();
	});

	it("says a provider refusal plainly without showing untrusted error text", () => {
		h.params = new URLSearchParams({
			error: "access_denied",
			error_description: "user cancelled",
		});

		render(<CallbackPage />);

		expect(
			screen.getByText(/Your sign-in provider did not sign you in/),
		).toBeTruthy();
		// Query-string diagnostics must not become page content.
		expect(screen.queryByText(/access_denied: user cancelled/)).toBeNull();
		expect(h.completeOAuth).not.toHaveBeenCalled();
	});

	it("offers the way back to sign-in as a link when the return carries nothing to redeem", () => {
		h.params = new URLSearchParams();

		render(<CallbackPage />);

		expect(
			screen.getByText("This sign-in link is incomplete. Start again."),
		).toBeTruthy();
		expect(
			screen.getByRole("link", { name: "Sign in again" }).getAttribute("href"),
		).toBe("/auth/login");
		expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
		expect(h.completeOAuth).not.toHaveBeenCalled();
	});

	it("shows the in-progress state while the exchange runs", () => {
		render(<CallbackPage />);

		expect(screen.getByText(/Signing you in/)).toBeTruthy();
	});
});

it("explains who grants access instead of offering sign-in again after a refusal", async () => {
	h.completeOAuth.mockRejectedValue(
		new AuthError("Access refused", { status: 403, code: "permission_denied" }),
	);
	render(<CallbackPage />);
	await screen.findByText(
		/Ask your organization administrator to grant access/,
	);
	expect(screen.queryByRole("link", { name: "Sign in again" })).toBeNull();
	expect(screen.queryByText("permission_denied")).toBeNull();
});
