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

afterEach(cleanup);

describe("CallbackPage", () => {
	it("redeems the authorization code once even when completeOAuth changes identity", () => {
		const { rerender } = render(<CallbackPage />);
		rerender(<CallbackPage />);
		rerender(<CallbackPage />);

		expect(h.completeOAuth).toHaveBeenCalledTimes(1);
		expect(h.completeOAuth).toHaveBeenCalledWith("the-code", "the-state");
	});

	it("renders friendly copy plus an operator reference when the exchange fails", async () => {
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
		expect(
			screen.getByText(/unauthenticated · request req-abc123/),
		).toBeTruthy();
	});

	it("surfaces a state/CSRF guard message with no reference line", async () => {
		h.completeOAuth.mockRejectedValue(
			new Error("OAuth state mismatch — possible CSRF attack"),
		);

		render(<CallbackPage />);

		await waitFor(() =>
			expect(screen.getByText(/possible CSRF attack/)).toBeTruthy(),
		);
		expect(screen.queryByText(/Reference:/)).toBeNull();
	});

	it("surfaces the identity provider's own error verbatim", () => {
		h.params = new URLSearchParams({
			error: "access_denied",
			error_description: "user cancelled",
		});

		render(<CallbackPage />);

		expect(screen.getByText(/access_denied: user cancelled/)).toBeTruthy();
		expect(h.completeOAuth).not.toHaveBeenCalled();
	});

	it("shows the in-progress state while the exchange runs", () => {
		render(<CallbackPage />);

		expect(screen.getByText(/Signing you in/)).toBeTruthy();
	});
});
