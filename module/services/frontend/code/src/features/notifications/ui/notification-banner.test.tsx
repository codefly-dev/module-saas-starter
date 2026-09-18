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
	organizationId: "org-1",
	hasAction: false,
	markRead: vi.fn(async () => undefined),
	resolveAction: vi.fn(async () => "/s/example-solution"),
	push: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: mocks.organizationId }),
}));
vi.mock("next/navigation", () => ({
	useRouter: () => ({ push: mocks.push }),
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
vi.mock("../service/mutations", () => ({
	notificationMutations: {
		markRead: mocks.markRead,
		resolveAction: mocks.resolveAction,
	},
}));
vi.mock("../service/queries", () => ({
	notificationQueries: {
		list: () => ({
			queryKey: ["notifications", 100],
			queryFn: async () => ({
				notifications: [
					{
						id: "other-org",
						orgId: "org-2",
						title: "Not for this organization",
						body: "Hidden",
						read: false,
						hasAction: false,
					},
					{
						id: "sync-complete",
						orgId: "org-1",
						title: "Source updated",
						body: "2 document changes are ready.",
						read: false,
						hasAction: mocks.hasAction,
					},
				],
			}),
		}),
	},
}));

import { NotificationBanner } from "./notification-banner";

afterEach(() => {
	cleanup();
	mocks.markRead.mockClear();
	mocks.resolveAction.mockClear();
	mocks.push.mockClear();
	mocks.hasAction = false;
});

function renderBanner() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<NotificationBanner />
		</QueryClientProvider>,
	);
}

describe("NotificationBanner", () => {
	it("shows only the newest unread notification in the active organization", async () => {
		renderBanner();
		expect(await screen.findByText("Source updated")).toBeTruthy();
		expect(screen.getByText("2 document changes are ready.")).toBeTruthy();
		expect(screen.queryByText("Not for this organization")).toBeNull();
	});

	it("dismisses through the host unread lifecycle", async () => {
		renderBanner();
		fireEvent.click(
			await screen.findByRole("button", { name: "Dismiss notification" }),
		);
		await waitFor(() =>
			expect(mocks.markRead).toHaveBeenCalledWith("sync-complete"),
		);
	});

	it("re-authorizes an action before marking it read and navigating", async () => {
		mocks.hasAction = true;
		renderBanner();
		fireEvent.click(await screen.findByRole("button", { name: "View" }));
		await waitFor(() =>
			expect(mocks.resolveAction).toHaveBeenCalledWith("sync-complete"),
		);
		await waitFor(() =>
			expect(mocks.markRead).toHaveBeenCalledWith("sync-complete"),
		);
		expect(mocks.push).toHaveBeenCalledWith("/s/example-solution");
	});
});
