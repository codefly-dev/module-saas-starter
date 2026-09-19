import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	pagination: false,
	markRead: vi.fn(async () => undefined),
	resolveAction: vi.fn(
		async (_v: { id: string; unread: boolean }) =>
			"/invitations/accept?token=token",
	),
	push: vi.fn(),
	error: vi.fn(),
}));

vi.mock("sonner", () => ({
	toast: { success: vi.fn(), error: mocks.error },
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: mocks.push }),
}));

vi.mock("../service/queries", () => ({
	notificationQueries: {
		list: (_size: number, options: { pageToken?: string } = {}) => ({
			queryKey: ["notifications", options.pageToken],
			queryFn: async () => ({
				notifications: [
					{
						id: "notification-1",
						title: options.pageToken
							? "Earlier notification"
							: "You've been invited",
						body: "Join Acme",
						type: "info",
						read: false,
						createdAt: "2026-07-28T12:34:56.789Z",
						hasAction: true,
					},
				],
				nextPageToken: mocks.pagination && !options.pageToken ? "older" : "",
			}),
		}),
	},
}));

vi.mock("../service/mutations", () => ({
	notificationMutations: {
		markRead: mocks.markRead,
		resolveAction: mocks.resolveAction,
		markAllRead: vi.fn(async () => undefined),
		delete: vi.fn(async () => undefined),
	},
}));

import { NotificationsPage } from "./notifications-page";

afterEach(() => {
	cleanup();
	mocks.pagination = false;
	mocks.markRead.mockClear();
	mocks.resolveAction.mockClear();
	mocks.push.mockClear();
	mocks.error.mockClear();
	mocks.resolveAction.mockResolvedValue("/invitations/accept?token=token");
});

function renderPage() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<NotificationsPage />
		</QueryClientProvider>,
	);
}

describe("NotificationsPage", () => {
	it("can reach older notifications and return to the first page", async () => {
		mocks.pagination = true;
		renderPage();
		fireEvent.click(
			await screen.findByRole("button", { name: "Older notifications" }),
		);
		expect(await screen.findByText("Earlier notification")).toBeTruthy();
		expect(
			screen.queryByRole("button", { name: "Older notifications" }),
		).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: "Newer notifications" }),
		);
		expect(await screen.findByText("You've been invited")).toBeTruthy();
	});
	it("opens the re-authorized destination and only then marks it read", async () => {
		renderPage();

		fireEvent.click(
			await screen.findByRole("button", { name: /You've been invited/ }),
		);

		await waitFor(() => {
			expect(mocks.push).toHaveBeenCalledWith(
				"/invitations/accept?token=token",
			);
		});
		expect(mocks.resolveAction).toHaveBeenCalledWith("notification-1");
		expect(mocks.markRead).toHaveBeenCalledWith("notification-1");
	});

	// A click the server refuses must not consume the item's unread state, and
	// must not fall back to any destination the page still holds.
	it("neither navigates nor marks read when the destination no longer resolves", async () => {
		mocks.resolveAction.mockRejectedValue(
			new ConnectError("notification not found", Code.NotFound),
		);
		renderPage();

		fireEvent.click(
			await screen.findByRole("button", { name: /You've been invited/ }),
		);

		await waitFor(() => {
			expect(mocks.error).toHaveBeenCalled();
		});
		expect(mocks.push).not.toHaveBeenCalled();
		expect(mocks.markRead).not.toHaveBeenCalled();
	});
});
