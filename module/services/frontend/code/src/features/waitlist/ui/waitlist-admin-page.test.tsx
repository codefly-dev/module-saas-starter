import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WaitlistState } from "@/gen/saas/accounts/v1/waitlist_pb";

const waitlistClient = vi.hoisted(() => ({
	list: vi.fn(({ pageToken }: { pageToken?: string }) =>
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

vi.mock("@connectrpc/connect", async (importOriginal) => ({
	...(await importOriginal<typeof import("@connectrpc/connect")>()),
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

it("requires confirmation before rejecting a named access request", async () => {
	const response = await waitlistClient.list({});
	waitlistClient.list.mockResolvedValueOnce({
		...response,
		entries: response.entries.map((entry) => ({
			...entry,
			state: WaitlistState.PENDING,
		})),
	});
	waitlistClient.review.mockResolvedValue({});
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<WaitlistAdminPage />
		</QueryClientProvider>,
	);
	fireEvent.click(
		await screen.findByRole("button", { name: "Reject first@example.com" }),
	);
	expect(await screen.findByRole("alertdialog")).toBeTruthy();
	expect(waitlistClient.review).not.toHaveBeenCalled();
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(waitlistClient.review).not.toHaveBeenCalled();
	fireEvent.click(
		screen.getByRole("button", { name: "Reject first@example.com" }),
	);
	fireEvent.click(
		await screen.findByRole("button", { name: "Reject request" }),
	);
	await waitFor(() =>
		expect(waitlistClient.review).toHaveBeenCalledWith(
			expect.objectContaining({ id: "entry-1", state: WaitlistState.REJECTED }),
		),
	);
});
