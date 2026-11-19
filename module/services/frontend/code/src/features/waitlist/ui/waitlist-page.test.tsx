import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AcquisitionMode } from "@/gen/saas/accounts/v1/waitlist_pb";

const client = vi.hoisted(() => ({
	getAcquisitionStatus: vi.fn(),
	join: vi.fn(),
}));
vi.mock("@connectrpc/connect", async (original) => ({
	...(await original<typeof import("@connectrpc/connect")>()),
	createClient: () => client,
}));
vi.mock("next/navigation", () => ({
	useSearchParams: () => new URLSearchParams(),
}));

import { WaitlistPage } from "./waitlist-page";

afterEach(cleanup);
beforeEach(() => {
	client.getAcquisitionStatus
		.mockReset()
		.mockResolvedValue({
			mode: AcquisitionMode.APPROVAL_REQUIRED,
			consentPolicyVersion: "policy",
		});
	client.join
		.mockReset()
		.mockResolvedValue({ message: "internal server text" });
});
describe("request access", () => {
	it("does not show an editable form until availability is known", async () => {
		let resolve = (_: unknown) => {};
		client.getAcquisitionStatus.mockReturnValue(
			new Promise((r) => {
				resolve = r;
			}),
		);
		render(<WaitlistPage />);
		expect(screen.queryByLabelText(/Work email/)).toBeNull();
		await act(async () => resolve({ mode: AcquisitionMode.CLOSED }));
		expect(
			screen.getByRole("heading", { name: "Registration is closed" }),
		).toBeTruthy();
		expect(screen.queryByLabelText(/Work email/)).toBeNull();
	});
	it("shows a safe failed-read state and recovers on retry", async () => {
		client.getAcquisitionStatus.mockRejectedValueOnce(
			new Error("private diagnostics"),
		);
		render(<WaitlistPage />);
		await screen.findByText("Couldn’t check registration availability");
		expect(screen.queryByLabelText(/Work email/)).toBeNull();
		expect(screen.queryByText("private diagnostics")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Try again" }));
		await screen.findByLabelText(/Work email/);
	});
	it("validates email beside its field and retains the form after a failed request", async () => {
		client.join.mockRejectedValue(new Error("private diagnostics"));
		render(<WaitlistPage />);
		const email = (await screen.findByLabelText(
			/Work email/,
		)) as HTMLInputElement;
		fireEvent.change(email, { target: { value: "invalid" } });
		fireEvent.click(screen.getByRole("button", { name: "Join waitlist" }));
		await screen.findByText("Enter a valid email address.");
		expect(email.getAttribute("aria-invalid")).toBe("true");
		expect(client.join).not.toHaveBeenCalled();
		fireEvent.change(email, { target: { value: "person@example.com" } });
		fireEvent.change(screen.getByLabelText("Company"), {
			target: { value: "Acme" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Join waitlist" }));
		await screen.findByText("Couldn’t request access");
		expect(email.value).toBe("person@example.com");
		expect((screen.getByLabelText("Company") as HTMLInputElement).value).toBe(
			"Acme",
		);
		expect(screen.queryByText("private diagnostics")).toBeNull();
	});
	it("locks fields during submission and shows a human acknowledgement", async () => {
		let resolve = (_: unknown) => {};
		client.join.mockReturnValue(
			new Promise((r) => {
				resolve = r;
			}),
		);
		render(<WaitlistPage />);
		const email = (await screen.findByLabelText(
			/Work email/,
		)) as HTMLInputElement;
		fireEvent.change(email, { target: { value: "person@example.com" } });
		fireEvent.click(screen.getByRole("button", { name: "Join waitlist" }));
		await waitFor(() => expect(email.disabled).toBe(true));
		expect(
			(screen.getByLabelText("Company") as HTMLInputElement).disabled,
		).toBe(true);
		expect(
			(
				screen.getByRole("button", {
					name: "Requesting access…",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		await act(async () => resolve({ message: "internal server text" }));
		expect(
			screen.getByRole("heading", { name: "Request received" }),
		).toBeTruthy();
		expect(screen.queryByText("internal server text")).toBeNull();
	});
});
