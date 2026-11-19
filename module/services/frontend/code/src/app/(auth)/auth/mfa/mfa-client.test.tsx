import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
	completeMFA: vi.fn(),
	completeMFAWithPasskey: vi.fn(),
	cancelMFA: vi.fn(),
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => h }));

import MFAClient from "./mfa-client";

afterEach(() => {
	cleanup();
	vi.resetAllMocks();
});
it("locks code submission and cancellation during a passkey challenge", async () => {
	let fail!: (error: Error) => void;
	h.completeMFAWithPasskey.mockImplementationOnce(
		() =>
			new Promise((_resolve, reject) => {
				fail = reject;
			}),
	);
	render(<MFAClient />);
	const code = screen.getByLabelText("Authentication code");
	fireEvent.change(code, { target: { value: "123456" } });
	fireEvent.click(screen.getByRole("button", { name: "Use a passkey" }));
	expect(code.hasAttribute("disabled")).toBe(true);
	expect(
		screen
			.getByRole("button", { name: "Verify and continue" })
			.hasAttribute("disabled"),
	).toBe(true);
	expect(
		screen
			.getByRole("button", { name: "Back to sign in" })
			.hasAttribute("disabled"),
	).toBe(true);
	fireEvent.submit(code.closest("form")!);
	expect(h.completeMFA).not.toHaveBeenCalled();
	fail(new Error("private diagnostics"));
	await waitFor(() => expect(code.hasAttribute("disabled")).toBe(false));
	expect(screen.queryByText("private diagnostics")).toBeNull();
});
