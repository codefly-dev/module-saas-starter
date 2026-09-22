import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { EventOperationsPage } from "./event-operations-page";

// Both reads route through PlatformAdminService (saas.accounts.v1), even though
// the message types live in saas.events.v1.
afterEach(cleanup);

describe("EventOperationsPage admin container", () => {
	it("renders the event catalog and subscriptions the service returns", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetEventOperations"), () =>
				HttpResponse.json({
					eventTypes: [
						{
							type: "saas.accounts.v1.OrganizationCreated",
							visibility: "external",
							publisher: "accounts",
							major: 1,
							totalEvents: "12",
							unpublished: "0",
							subscribers: "1",
						},
					],
					relay: {
						totalEvents: "12",
						publishedEvents: "12",
						backlog: "0",
					},
					deadLetters: [],
				}),
			),
			http.post(rpc("PlatformAdminService", "ListEventSubscriptions"), () =>
				HttpResponse.json({
					subscriptions: [
						{
							id: "sub-1",
							subscriberPrincipalId: "11111111-1111-1111-1111-111111111111",
							typePattern: "saas.accounts.v1.*",
							queue: "webhooks",
							delivery: "at_least_once",
							queueDeadLetter: "0",
						},
					],
				}),
			),
		);
		renderInApp(<EventOperationsPage />);
		expect(
			await screen.findByText("saas.accounts.v1.OrganizationCreated"),
		).toBeTruthy();
		expect(await screen.findByText("saas.accounts.v1.*")).toBeTruthy();
	});
});
