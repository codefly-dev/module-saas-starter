import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { PasskeySetup } from "./passkey-setup";

const begin = vi.hoisted(() => vi.fn());
vi.mock("@simplewebauthn/browser", () => ({
	browserSupportsWebAuthn: () => true,
	startRegistration: vi.fn(),
}));
vi.mock("../service/mutations", () => ({
	mfaMutations: {
		beginWebAuthnRegistration: begin,
		finishWebAuthnRegistration: vi.fn(),
	},
}));
afterEach(() => {
	cleanup();
	vi.clearAllMocks();
});
it("keeps a rejected registration visible without exposing server text", async () => {
	begin.mockRejectedValueOnce(
		new ConnectError("private credential diagnostics", Code.PermissionDenied),
	);
	const onClose = vi.fn();
	renderInApp(<PasskeySetup open onClose={onClose} />);
	fireEvent.click(screen.getByRole("button", { name: "Create passkey" }));
	expect(await screen.findByText("Passkey wasn’t added")).toBeTruthy();
	expect(screen.getByText(/Ask your organization administrator/)).toBeTruthy();
	expect(screen.queryByText(/private credential diagnostics/)).toBeNull();
	expect(onClose).not.toHaveBeenCalled();
});
it("keeps the dialog and field disabled during registration", async () => {
	begin.mockImplementationOnce(() => new Promise(() => {}));
	const onClose = vi.fn();
	renderInApp(<PasskeySetup open onClose={onClose} />);
	fireEvent.click(screen.getByRole("button", { name: "Create passkey" }));
	await waitFor(() =>
		expect(
			(screen.getByLabelText("Passkey name") as HTMLInputElement).disabled,
		).toBe(true),
	);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(onClose).not.toHaveBeenCalled();
});
