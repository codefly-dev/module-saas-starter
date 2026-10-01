import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	info: vi.fn(),
	unread: 1,
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ getToken: () => "token" }),
}));
vi.mock("sonner", () => ({ toast: { info: mocks.info } }));
vi.mock("./notification-panel", () => ({ NotificationPanel: () => null }));
vi.mock("../service/queries", () => ({
	notificationQueries: {
		unreadCount: () => ({
			queryKey: ["notifications", "unread-count"],
			queryFn: async () => ({ count: mocks.unread }),
		}),
	},
}));

import { NotificationBell } from "./notification-bell";

// The stream is unavailable, so the bell runs on its polled count alone.
beforeEach(() => {
	vi.stubGlobal(
		"fetch",
		vi.fn(async () => new Response(null, { status: 503 })),
	);
});

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	mocks.info.mockClear();
	mocks.unread = 1;
});

function renderBell(queryClient: QueryClient) {
	render(
		<QueryClientProvider client={queryClient}>
			<NotificationBell />
		</QueryClientProvider>,
	);
}

describe("NotificationBell", () => {
	// One old unread notification used to toast "You have a new notification"
	// on every page load: the baseline started at the loading placeholder of 0,
	// so the first real count read as an arrival.
	it("does not announce what was already unread when the page loads", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		renderBell(queryClient);
		expect(await screen.findByText("1")).toBeTruthy();

		// A reload, or a navigation into the other shell, mounts a fresh bell.
		cleanup();
		renderBell(
			new QueryClient({ defaultOptions: { queries: { retry: false } } }),
		);
		expect(await screen.findByText("1")).toBeTruthy();
		expect(mocks.info).not.toHaveBeenCalled();
	});

	it("announces a notification that arrives during the session", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		renderBell(queryClient);
		expect(await screen.findByText("1")).toBeTruthy();

		act(() => {
			queryClient.setQueryData(["notifications", "unread-count"], {
				count: 3,
			});
		});
		await waitFor(() =>
			expect(mocks.info).toHaveBeenCalledWith("You have 2 new notifications"),
		);
		expect(mocks.info).toHaveBeenCalledTimes(1);
	});
});
