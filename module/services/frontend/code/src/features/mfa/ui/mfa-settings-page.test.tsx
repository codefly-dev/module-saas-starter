import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../service/queries", () => ({
	mfaQueries: {
		devices: () => ({
			queryKey: ["mfa", "devices"],
			queryFn: async () => ({
				devices: [
					{
						id: "device-1",
						name: "Antoine's iPhone",
						type: "webauthn",
						createdAt: "2026-01-01T00:00:00Z",
						lastUsedAt: "2026-02-01T00:00:00Z",
					},
				],
			}),
		}),
	},
}));

import { MFASettingsPage } from "./mfa-settings-page";

afterEach(cleanup);

describe("MFASettingsPage admin container", () => {
	it("renders the enrolled device the MFA service returns", async () => {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={client}>
				<MFASettingsPage />
			</QueryClientProvider>,
		);

		expect(await screen.findByText("Antoine's iPhone")).toBeTruthy();
	});
});
