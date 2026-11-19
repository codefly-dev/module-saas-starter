import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

const logout = vi.hoisted(() => vi.fn());
vi.mock("@/lib/auth", () => ({ useAuth: () => ({ logout }) }));

import { LogoutPage } from "./logout-page";

afterEach(() => {
	cleanup();
	logout.mockReset();
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
