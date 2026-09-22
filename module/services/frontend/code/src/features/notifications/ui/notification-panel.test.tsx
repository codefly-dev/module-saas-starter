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
						hasAction: true,
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
		resolveAction: mocks.resolveAction,
		markAllRead: vi.fn(async () => undefined),
	},
}));

import { NotificationPanel } from "./notification-panel";

afterEach(() => {
	cleanup();
	mocks.markRead.mockClear();
	mocks.resolveAction.mockClear();
	mocks.push.mockClear();
	mocks.error.mockClear();
	mocks.resolveAction.mockResolvedValue("/invitations/accept?token=token");
});

function renderPanel(onClose = vi.fn()) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<NotificationPanel onClose={onClose} />
		</QueryClientProvider>,
	);
	return onClose;
}

describe("NotificationPanel", () => {
	// The list carries no destination at all: it comes back from the server,
	// which re-authorizes the resource as the link is followed.
	it("opens the re-authorized destination and only then marks it read", async () => {
		const onClose = renderPanel();

		fireEvent.click(
			await screen.findByRole("button", {
				name: /You've been invited/,
			}),
		);

		await waitFor(() => {
			expect(mocks.push).toHaveBeenCalledWith("/invitations/accept?token=token");
		});
		expect(mocks.resolveAction).toHaveBeenCalledWith("notification-1");
		expect(mocks.markRead).toHaveBeenCalledWith("notification-1");
		expect(onClose).toHaveBeenCalledOnce();
	});

	// A link followed after the grant was revoked resolves to NOT_FOUND. The
	// panel must not navigate — and must not consume the item's unread state for
	// a click the server refused.
	it("neither navigates nor marks read when the destination no longer resolves", async () => {
		mocks.resolveAction.mockRejectedValue(
			new ConnectError("notification not found", Code.NotFound),
		);
		const onClose = renderPanel();

		fireEvent.click(
			await screen.findByRole("button", {
				name: /You've been invited/,
			}),
		);

		await waitFor(() => {
			expect(mocks.error).toHaveBeenCalled();
		});
		expect(mocks.push).not.toHaveBeenCalled();
		expect(mocks.markRead).not.toHaveBeenCalled();
		expect(onClose).not.toHaveBeenCalled();
	});

	// A destination that fails the shape gate rejects rather than resolving
	// undefined, so the click reports an error instead of doing nothing.
	it("reports an error when the destination is not a local path", async () => {
		mocks.resolveAction.mockRejectedValue(
			new Error("notification destination is not a local path"),
		);
		const onClose = renderPanel();

		fireEvent.click(
			await screen.findByRole("button", {
				name: /You've been invited/,
			}),
		);

		await waitFor(() => {
			expect(mocks.error).toHaveBeenCalled();
		});
		expect(mocks.push).not.toHaveBeenCalled();
		expect(mocks.markRead).not.toHaveBeenCalled();
		expect(onClose).not.toHaveBeenCalled();
	});
});
