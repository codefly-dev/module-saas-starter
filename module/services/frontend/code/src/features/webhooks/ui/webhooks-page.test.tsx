import {
	cleanup,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// The page scopes subscriptions to the active tenant from the session. Pin it
// so the container renders the table (not the "select an organization" state).
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "org-1",
		switchOrganization: async () => {},
	}),
}));

import { WebhooksPage } from "./webhooks-page";

afterEach(cleanup);

describe("WebhooksPage admin container", () => {
	it("renders the webhook subscriptions the service returns", async () => {
		server.use(
			http.post(rpc("WebhookService", "ListSubscriptions"), () =>
				HttpResponse.json({
					subscriptions: [
						{
							id: "wh-1",
							orgId: "org-1",
							url: "https://example.com/hooks/orders",
							description: "Order events",
							events: ["order.created"],
							active: true,
							createdAt: "2026-01-02T03:04:05Z",
						},
					],
				}),
			),
		);
		renderInApp(<WebhooksPage />);
		expect(
			await screen.findByText("https://example.com/hooks/orders"),
		).toBeTruthy();
	});
});

it.each([
	["Delete", "Delete webhook", "DeleteSubscription"],
	["Rotate secret", "Rotate secret", "RotateSecret"],
])(
	"confirms %s for the named webhook, with cancellation",
	async (menu, action, procedure) => {
		const mutate = vi.fn(() => HttpResponse.json({}));
		const url = "https://example.com/hooks/orders";
		server.use(
			http.post(rpc("WebhookService", "ListSubscriptions"), () =>
				HttpResponse.json({
					subscriptions: [
						{ id: "wh-1", orgId: "org-1", url, events: [], active: true },
					],
				}),
			),
			http.post(rpc("WebhookService", procedure), mutate),
		);
		renderInApp(<WebhooksPage />);
		const open = async () => {
			fireEvent.click(
				await screen.findByRole("button", { name: `Actions for ${url}` }),
			);
			fireEvent.click(await screen.findByRole("menuitem", { name: menu }));
			return screen.findByRole("alertdialog");
		};
		let dialog = await open();
		expect(within(dialog).getByText(url)).toBeTruthy();
		expect(mutate).not.toHaveBeenCalled();
		fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(mutate).not.toHaveBeenCalled();
		dialog = await open();
		fireEvent.click(within(dialog).getByRole("button", { name: action }));
		await waitFor(() => expect(mutate).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	},
);

it("offers one first action when no webhooks exist", async () => {
	server.use(
		http.post(rpc("WebhookService", "ListSubscriptions"), () =>
			HttpResponse.json({ subscriptions: [] }),
		),
	);
	renderInApp(<WebhooksPage />);
	expect(await screen.findByText("Connect your first webhook")).toBeTruthy();
	expect(
		screen.getAllByRole("button", { name: "Create webhook" }),
	).toHaveLength(1);
	fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
	expect(await screen.findByRole("dialog")).toBeTruthy();
});
