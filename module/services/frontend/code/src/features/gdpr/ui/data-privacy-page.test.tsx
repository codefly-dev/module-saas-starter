import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

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
