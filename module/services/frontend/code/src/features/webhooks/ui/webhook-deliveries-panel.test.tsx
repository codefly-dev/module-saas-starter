import { fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import type { WebhookSubscription } from "../model/types";
import { WebhookDeliveriesPanel } from "./webhook-deliveries-panel";

const subscription: WebhookSubscription = {
	id: "wh-1",
	description: "",
	createdAt: undefined,
	updatedAt: undefined,
	orgId: "org-1",
	url: "https://example.com/events",
	events: [],
	active: true,
};

it("shows a safe denied state without retry or empty messaging", async () => {
	server.use(
		http.post(rpc("WebhookService", "ListDeliveries"), () =>
			HttpResponse.json(
				{ code: "permission_denied", message: "private service diagnostic" },
				{ status: 403 },
			),
		),
	);
	renderInApp(
		<WebhookDeliveriesPanel subscription={subscription} onClose={() => {}} />,
	);
	expect(
		await screen.findByText(/Ask an organization administrator/),
	).toBeTruthy();
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	expect(screen.queryByText("No deliveries yet")).toBeNull();
	expect(screen.queryByText(/private service diagnostic/)).toBeNull();
});

it("uses the kit table and a labelled collapse control", async () => {
	server.use(
		http.post(rpc("WebhookService", "ListDeliveries"), () =>
			HttpResponse.json({
				deliveries: [
					{
						id: "d-1",
						eventType: "user.created",
						status: 2,
						attempts: 1,
						httpStatus: 200,
					},
				],
			}),
		),
	);
	renderInApp(
		<WebhookDeliveriesPanel subscription={subscription} onClose={() => {}} />,
	);
	expect(
		await screen.findByRole("table", { name: "Webhook deliveries" }),
	).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Collapse deliveries" }));
	expect(screen.queryByRole("table")).toBeNull();
	expect(
		screen
			.getByRole("button", { name: "Expand deliveries" })
			.getAttribute("aria-expanded"),
	).toBe("false");
});
