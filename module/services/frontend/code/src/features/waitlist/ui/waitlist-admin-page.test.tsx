import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WaitlistState } from "@/gen/saas/accounts/v1/waitlist_pb";

const waitlistClient = vi.hoisted(() => ({
	list: vi.fn(
		({ pageToken }: { pageToken?: string }) =>
			Promise.resolve(
				pageToken
					? {
							entries: [
								{
									id: "entry-2",
									email: "second@example.com",
									state: WaitlistState.APPROVED,
									tags: [],
									adminNotes: "",
									name: "",
									company: "",
									source: "",
									campaign: "",
								},
							],
							nextPageToken: "",
						}
					: {
							entries: [
								{
									id: "entry-1",
									email: "first@example.com",
									state: WaitlistState.APPROVED,
									tags: [],
									adminNotes: "",
									name: "",
									company: "",
									source: "",
									campaign: "",
								},
							],
							nextPageToken: "page-2",
						},
			),
	),
	review: vi.fn(),
	invite: vi.fn(),
}));

vi.mock("@connectrpc/connect", () => ({
	createClient: () => waitlistClient,
}));

import { WaitlistAdminPage } from "./waitlist-admin-page";

afterEach(() => {
	cleanup();
	waitlistClient.list.mockClear();
});

describe("WaitlistAdminPage", () => {
	it("follows the server page token instead of truncating after the first page", async () => {
		render(
			<QueryClientProvider
				client={
					new QueryClient({
						defaultOptions: { queries: { retry: false } },
					})
				}
			>
				<WaitlistAdminPage />
			</QueryClientProvider>,
		);

		await screen.findByText("first@example.com");
		fireEvent.click(screen.getByRole("button", { name: "Next" }));
		await screen.findByText("second@example.com");

		expect(waitlistClient.list).toHaveBeenLastCalledWith(
			expect.objectContaining({ pageToken: "page-2" }),
		);
	});
});
