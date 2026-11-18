import { cleanup, fireEvent, screen } from "@testing-library/react";
import { delay, HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { EventOperationsPage } from "./event-operations-page";

// Both reads route through PlatformAdminService (saas.accounts.v1), even though
// the message types live in saas.events.v1.
const admin = (method: string) => rpc("PlatformAdminService", method);

const ORGANIZATION_CREATED = {
	type: "saas.accounts.v1.OrganizationCreated",
	visibility: "external",
	publisher: "accounts",
	major: 1,
	totalEvents: "12",
	unpublished: "0",
	subscribers: "1",
};
const SUBSCRIPTION = {
	id: "sub-1",
	subscriberPrincipalId: "11111111-1111-1111-1111-111111111111",
	typePattern: "saas.accounts.v1.*",
	queue: "webhooks",
	delivery: "ordered",
	queueDeadLetter: "0",
};

function operationsAnswer(body: Record<string, unknown>) {
	return http.post(admin("GetEventOperations"), () => HttpResponse.json(body));
}
function subscriptionsAnswer(subscriptions: unknown[]) {
	return http.post(admin("ListEventSubscriptions"), () =>
		HttpResponse.json({ subscriptions }),
	);
}
function refusal(method: string) {
	return http.post(admin(method), () =>
		HttpResponse.json(
			{ code: "permission_denied", message: "platform:events:read required" },
			{ status: 403 },
		),
	);
}
function outage(method: string) {
	return http.post(admin(method), () =>
		HttpResponse.json(
			{ code: "internal", message: "pq: connection refused" },
			{ status: 500 },
		),
	);
}

afterEach(cleanup);

describe("EventOperationsPage admin container", () => {
	it("renders the event catalog and subscriptions the service returns", async () => {
		server.use(
			operationsAnswer({
				eventTypes: [ORGANIZATION_CREATED],
				relay: { totalEvents: "12", publishedEvents: "12", backlog: "0" },
				deadLetters: [],
			}),
			subscriptionsAnswer([SUBSCRIPTION]),
		);
		renderInApp(<EventOperationsPage />);
		expect(
			await screen.findByText("saas.accounts.v1.OrganizationCreated"),
		).toBeTruthy();
		expect(await screen.findByText("saas.accounts.v1.*")).toBeTruthy();
		expect(screen.getByText("External")).toBeTruthy();
		// The delivery mode is said in words, not as the stored value.
		expect(screen.getByText("In order")).toBeTruthy();
		expect(screen.queryByText("ordered")).toBeNull();
		// Nothing is waiting, so nothing says delivery is behind, and the
		// undelivered section stays away.
		expect(screen.queryByText(/Delivery is behind/)).toBeNull();
		expect(screen.queryByText("Undelivered events by queue")).toBeNull();
	});

	it("shows loading tables, and no empty state, while the reads are in flight", async () => {
		server.use(
			http.post(admin("GetEventOperations"), () => delay("infinite")),
			http.post(admin("ListEventSubscriptions"), () => delay("infinite")),
		);
		renderInApp(<EventOperationsPage />);
		expect(await screen.findAllByLabelText("Loading table")).toHaveLength(2);
		expect(screen.queryByText("No event types yet")).toBeNull();
		expect(screen.queryByText("No active subscriptions")).toBeNull();
	});

	it("says what an event type and a subscription are when there are none", async () => {
		server.use(
			operationsAnswer({ eventTypes: [], deadLetters: [] }),
			subscriptionsAnswer([]),
		);
		renderInApp(<EventOperationsPage />);
		expect(await screen.findByText("No event types yet")).toBeTruthy();
		expect(await screen.findByText("No active subscriptions")).toBeTruthy();
		expect(
			screen.getByText(
				/An event type is a kind of thing the platform announces/,
			),
		).toBeTruthy();
	});

	it("says who can grant access when the reads are refused, and offers no retry", async () => {
		server.use(
			refusal("GetEventOperations"),
			refusal("ListEventSubscriptions"),
		);
		renderInApp(<EventOperationsPage />);
		expect(
			await screen.findByText("You can't see the event types"),
		).toBeTruthy();
		expect(
			await screen.findByText("You can't see the subscriptions"),
		).toBeTruthy();
		expect(
			screen.getAllByText(/Ask a platform super administrator to grant it/),
		).toHaveLength(2);
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
		expect(screen.queryByText(/platform:events:read/)).toBeNull();
		expect(screen.queryByText("No event types yet")).toBeNull();
	});

	it("offers Retry after a real failure, without the server's text, and recovers", async () => {
		server.use(outage("GetEventOperations"), outage("ListEventSubscriptions"));
		renderInApp(<EventOperationsPage />);
		expect(
			await screen.findByText("Couldn't load the event types"),
		).toBeTruthy();
		expect(
			await screen.findByText("Couldn't load the subscriptions"),
		).toBeTruthy();
		expect(screen.queryByText(/connection refused/)).toBeNull();
		expect(screen.queryByText("No active subscriptions")).toBeNull();

		server.use(subscriptionsAnswer([SUBSCRIPTION]));
		fireEvent.click(screen.getAllByRole("button", { name: "Retry" })[1]);
		expect(await screen.findByText("saas.accounts.v1.*")).toBeTruthy();
	});

	it("says delivery is behind and lists what could not be delivered", async () => {
		server.use(
			operationsAnswer({
				eventTypes: [
					{ ...ORGANIZATION_CREATED, visibility: "", unpublished: "3" },
				],
				relay: {
					totalEvents: "12",
					publishedEvents: "9",
					backlog: "3",
					oldestUnpublishedAt: "2026-01-02T03:04:05Z",
				},
				deadLetters: [{ queue: "webhooks", deadLetter: "4" }],
			}),
			subscriptionsAnswer([{ ...SUBSCRIPTION, delivery: "at_least_once" }]),
		);
		renderInApp(<EventOperationsPage />);
		expect(
			await screen.findByText(
				/^Delivery is behind: 3 events are waiting to be sent, the oldest since .+\.$/,
			),
		).toBeTruthy();
		expect(screen.getByText("Undeclared")).toBeTruthy();
		expect(screen.getByText("Undelivered events by queue")).toBeTruthy();
		// A delivery mode this build does not know is not echoed.
		expect(await screen.findByText("Not recognized")).toBeTruthy();
		expect(screen.queryByText("at_least_once")).toBeNull();
	});
});
