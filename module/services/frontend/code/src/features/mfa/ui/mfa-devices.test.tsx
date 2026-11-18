import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { MFADevices } from "./mfa-devices";

it("names the device and keeps its confirmation open during removal and refusal", async () => {
	let reject!: (reason: unknown) => void;
	const remove = vi.fn(
		() =>
			new Promise<void>((_, fail) => {
				reject = fail;
			}),
	);
	render(
		<MFADevices
			data={[
				{
					id: "device-1",
					name: "Personal security key",
					type: "webauthn",
					createdAt: "2026-01-01",
					lastUsedAt: undefined,
				},
			]}
			isLoading={false}
			onRevoke={remove}
		/>,
	);
	fireEvent.click(
		screen.getByRole("button", { name: "Remove Personal security key" }),
	);
	expect(
		screen.getByRole("alertdialog", { name: "Remove Personal security key?" }),
	).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(remove).not.toHaveBeenCalled();
	fireEvent.click(
		screen.getByRole("button", { name: "Remove Personal security key" }),
	);
	fireEvent.click(
		screen.getByRole("button", { name: "Remove security method" }),
	);
	await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
	expect(
		(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	reject(new ConnectError("private device detail", Code.PermissionDenied));
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	expect(screen.queryByText(/private device detail/)).toBeNull();
});
