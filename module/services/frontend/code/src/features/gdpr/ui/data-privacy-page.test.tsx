import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mutations = vi.hoisted(() => ({
	requestDeletion: vi.fn(),
	requestExport: vi.fn(),
}));
vi.mock("../service/mutations", () => ({ gdprMutations: mutations }));

import { DataPrivacyPage } from "./data-privacy-page";

describe("DataPrivacyPage", () => {
	it("does not expose incomplete export or deletion as production actions", () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<DataPrivacyPage exportAvailable={false} deletionAvailable={false} />
			</QueryClientProvider>,
		);

		expect(
			(
				screen.getByRole("button", {
					name: "Request export",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		expect(
			(
				screen.getByRole("button", {
					name: "Delete account",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		// A disabled button alone does not say why. The person is told the
		// action is not offered here and what to do instead, in their words.
		expect(
			screen.getByText(/Data export is not available here yet/),
		).toBeTruthy();
		expect(
			screen.getByText(/Account deletion is not available here yet/),
		).toBeTruthy();
		expect(screen.queryByText(/adapter|starter|anonymization/i)).toBeNull();
		expect(
			screen.queryByText(/in accordance with GDPR regulations/i),
		).toBeNull();
		expect(
			screen.queryByText(
				/permanently delete your account and all associated data/i,
			),
		).toBeNull();
	});

	it("enables actions only when the server projection makes them available", () => {
		const queryClient = new QueryClient();
		render(
			<QueryClientProvider client={queryClient}>
				<DataPrivacyPage exportAvailable deletionAvailable />
			</QueryClientProvider>,
		);

		expect(
			(
				screen.getByRole("button", {
					name: "Request export",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
		expect(
			(
				screen.getByRole("button", {
					name: "Delete account",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
		expect(screen.queryByText(/not available here yet/)).toBeNull();
	});
});

it("keeps account deletion confirmation open during the request and on refusal", async () => {
	let reject!: (reason: unknown) => void;
	mutations.requestDeletion.mockImplementationOnce(
		() =>
			new Promise((_, fail) => {
				reject = fail;
			}),
	);
	render(
		<QueryClientProvider client={new QueryClient()}>
			<DataPrivacyPage exportAvailable deletionAvailable />
		</QueryClientProvider>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Delete account" }));
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(mutations.requestDeletion).not.toHaveBeenCalled();
	fireEvent.click(screen.getByRole("button", { name: "Delete account" }));
	fireEvent.click(
		screen.getByRole("button", { name: "Yes, delete my account" }),
	);
	await waitFor(() =>
		expect(mutations.requestDeletion).toHaveBeenCalledTimes(1),
	);
	expect(
		(screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	reject(new ConnectError("private account detail", Code.PermissionDenied));
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(screen.getByRole("alertdialog")).toBeTruthy();
	expect(screen.queryByText(/private account detail/)).toBeNull();
});
