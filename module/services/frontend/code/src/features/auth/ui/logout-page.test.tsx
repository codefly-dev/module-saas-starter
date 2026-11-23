import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

const logout = vi.hoisted(() => vi.fn());
const session = vi.hoisted(() => ({ isLoading: false }));
// A fresh function each render, as the real logout becomes whenever the session
// it reads changes.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		logout: () => logout(),
		isLoading: session.isLoading,
	}),
}));

import { LogoutPage, resetSignOutForTests } from "./logout-page";

afterEach(() => {
	cleanup();
	logout.mockReset();
	resetSignOutForTests();
	session.isLoading = false;
	vi.unstubAllGlobals();
});
it("signs out only once the provider is done loading", async () => {
	logout.mockImplementation(() => new Promise(() => {}));
	session.isLoading = true;
	const { rerender } = render(<LogoutPage />);
	expect(screen.getByRole("heading", { name: "Signing out" })).toBeTruthy();
	expect(logout).not.toHaveBeenCalled();

	session.isLoading = false;
	rerender(<LogoutPage />);
	await waitFor(() => expect(logout).toHaveBeenCalledTimes(1));
});
it("signs out once even when logout changes identity, and returns to sign-in", async () => {
	const replace = vi.fn();
	vi.stubGlobal("location", { replace });
	logout.mockResolvedValue({ complete: true });
	const { rerender } = render(<LogoutPage />);
	rerender(<LogoutPage />);
	rerender(<LogoutPage />);

	await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/login"));
	expect(replace).toHaveBeenCalledTimes(1);
	expect(logout).toHaveBeenCalledTimes(1);
});
// The shell rebuilds what is beneath it when the session's authority changes,
// and a sign-out changes it: the rebuilt page must not sign out again.
it("signs out once even when the page is rebuilt around the sign-out", async () => {
	const replace = vi.fn();
	vi.stubGlobal("location", { replace });
	logout.mockResolvedValue({ complete: true });
	const first = render(<LogoutPage />);
	await waitFor(() => expect(logout).toHaveBeenCalledTimes(1));
	first.unmount();
	render(<LogoutPage />);

	await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/login"));
	expect(logout).toHaveBeenCalledTimes(1);
	expect(
		screen.queryByRole("heading", { name: "Sign-out incomplete" }),
	).toBeNull();
});
it("says an incomplete sign-out on the rebuilt page too, without another attempt", async () => {
	logout.mockResolvedValue({ complete: false });
	const first = render(<LogoutPage />);
	await screen.findByRole("button", { name: "Try again" });
	first.unmount();
	render(<LogoutPage />);

	await screen.findByRole("heading", { name: "Sign-out incomplete" });
	expect(logout).toHaveBeenCalledTimes(1);
});
it("keeps an incomplete sign-out explicit and disables repeat actions while retrying", async () => {
	logout
		.mockResolvedValueOnce({ complete: false })
		.mockImplementationOnce(() => new Promise(() => {}));
	render(<LogoutPage />);
	const retry = await screen.findByRole("button", { name: "Try again" });
	expect(screen.getByText(/session may still be usable/)).toBeTruthy();
	fireEvent.click(retry);
	await waitFor(() => expect(logout).toHaveBeenCalledTimes(2));
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	expect(screen.getByRole("heading", { name: "Signing out" })).toBeTruthy();
});
it("does not expose a raw sign-out error", async () => {
	logout.mockRejectedValueOnce(new Error("private diagnostics"));
	render(<LogoutPage />);
	await screen.findByRole("button", { name: "Try again" });
	expect(screen.queryByText("private diagnostics")).toBeNull();
});
