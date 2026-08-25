import { cleanup, screen } from "@testing-library/react";
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
