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
	markRead: vi.fn(async () => undefined),
	push: vi.fn(),
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: mocks.push }),
}));

vi.mock("../service/queries", () => ({
	notificationQueries: {
		list: () => ({
			queryKey: ["notifications"],
			queryFn: async () => ({
				notifications: [
					{
						id: "notification-1",
						title: "You've been invited",
						body: "Join Acme",
						type: "info",
						read: false,
						createdAt: "2026-07-28T12:34:56.789Z",
						actionUrl: "/invitations/accept?token=token",
					},
				],
				nextPageToken: "",
			}),
		}),
	},
}));

vi.mock("../service/mutations", () => ({
	notificationMutations: {
		markRead: mocks.markRead,
		markAllRead: vi.fn(async () => undefined),
	},
}));

import { NotificationPanel } from "./notification-panel";

afterEach(() => {
	cleanup();
	mocks.markRead.mockClear();
	mocks.push.mockClear();
});

describe("NotificationPanel", () => {
	it("marks an actionable notification read and opens its destination", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const onClose = vi.fn();
		render(
			<QueryClientProvider client={queryClient}>
				<NotificationPanel onClose={onClose} />
			</QueryClientProvider>,
		);

		fireEvent.click(
			await screen.findByRole("button", {
				name: /You've been invited/,
			}),
		);

		await waitFor(() => {
			expect(mocks.markRead).toHaveBeenCalledWith("notification-1");
		});
		expect(onClose).toHaveBeenCalledOnce();
		expect(mocks.push).toHaveBeenCalledWith("/invitations/accept?token=token");
	});
});
