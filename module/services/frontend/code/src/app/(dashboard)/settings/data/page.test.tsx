import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import Page from "./page";

vi.mock("server-only", () => ({}));
vi.mock("@/features/trust/model/capabilities.server", () => ({
	loadPublicCapabilities: () => [
		{
			id: "privacy.export-artifact",
			state: "externally_attested",
		},
		{
			id: "privacy.deletion-completion",
			state: "externally_attested",
		},
	],
}));

describe("Data privacy settings page", () => {
	it("accepts an externally attested state wherever verification is required", () => {
		render(
			<QueryClientProvider client={new QueryClient()}>
				<Page />
			</QueryClientProvider>,
		);

		expect(
			(
				screen.getByRole("button", {
					name: "Request Export",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
		expect(
			(
				screen.getByRole("button", {
					name: "Delete Account",
				}) as HTMLButtonElement
			).disabled,
		).toBe(false);
	});
});
