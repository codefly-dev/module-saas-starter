import type { DataGraph } from "@codefly/saas-plugin-manifest";
import {
	act,
	cleanup,
	fireEvent,
	screen,
	waitFor,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

import { SolutionDashboards } from "../SolutionDashboard";

const { authState } = vi.hoisted(() => ({
	authState: {
		organizationId: "org-1" as string | undefined,
		user: { id: "user-1" },
	},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));

beforeEach(() => window.localStorage.clear());
afterEach(() => {
	cleanup();
	window.localStorage.clear();
});

// Two sections; the number (by event type, 37) is the only tile that can draw 37.
const graph: DataGraph = {
	events: [{ name: "login", type: "auth.login.v1" }],
	metrics: [
		{
			id: "per_day",
			kind: "source",
			filter: { event: "login" },
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		},
		{
			id: "by_type",
			kind: "source",
			filter: { event: "login" },
			groupBy: "event_type",
			aggregation: "count",
		},
	],
	dashboards: [
		{
			id: "activity",
			title: "Activity",
			layout: "grid",
			sections: [
				{ id: "overview", title: "Overview" },
				{ id: "trends", title: "Trends" },
			],
			widgets: [
				{
					id: "w_stat",
					metric: "by_type",
					visualization: "number",
					title: "Total logins",
					section: "overview",
				},
				{
					id: "w_line",
					metric: "per_day",
					visualization: "line",
					title: "Logins over time",
					section: "trends",
				},
			],
		},
	],
};

type Mode = "ok" | 403 | 503;
let mode: Mode = "ok";
let gate: Promise<void> | null = null;
function audit() {
	server.use(
		http.post(rpc("AuditService", "AggregateAuditLog"), async ({ request }) => {
			const body = (await request.json()) as { groupBy?: string };
			if (gate) await gate;
			if (mode === 403)
				return HttpResponse.json(
					{ code: "permission_denied" },
					{ status: 403 },
				);
			if (mode === 503)
				return HttpResponse.json({ code: "unavailable" }, { status: 503 });
			return HttpResponse.json({
				buckets:
					body.groupBy === "event_type"
						? [{ key: "saas.auth.login", count: "37" }]
						: [{ key: "2026-08-01", count: "5" }],
			});
		}),
	);
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 20));

// A + menu asks the same query a tile does, so a refusal it meets must delete
// the tile's retained values exactly as a tile's own refusal does (#1043):
// nothing restorable may be left for a later ordinary failure to hand back.
describe("a refusal met by a + menu", () => {
	it("deletes the tile's retained values: 200, removed, 403 in the menu, then 503 on add-back never shows them", async () => {
		mode = "ok";
		audit();
		const { client } = renderInApp(
			<SolutionDashboards graph={graph} solutionId="example" />,
		);
		expect(await screen.findAllByText("37")).not.toHaveLength(0);

		fireEvent.click(
			screen.getByRole("button", { name: "Remove Total logins" }),
		);
		expect(screen.queryAllByText("37")).toHaveLength(0);

		// The viewer loses access; the only read is the + menu's.
		mode = 403;
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Trends" }),
		);
		const item = await screen.findByRole("menuitem", { name: "Total logins" });
		await waitFor(() => expect(client.isFetching()).toBe(0));
		await act(settle);
		// No hint: a refusal is not "No data yet".
		expect(item.hasAttribute("aria-describedby")).toBe(false);
		expect(item.textContent).toBe("Total logins");

		// Then an ordinary failure, with no authorized read between.
		mode = 503;
		fireEvent.click(item);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(screen.queryAllByText("37")).toHaveLength(0);
	});

	it("deletes them when the refusal lands after the menu has closed", async () => {
		mode = "ok";
		gate = null;
		audit();
		const { client } = renderInApp(
			<SolutionDashboards graph={graph} solutionId="example" />,
		);
		expect(await screen.findAllByText("37")).not.toHaveLength(0);
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Total logins" }),
		);

		let release = () => {};
		gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		mode = 403;
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Overview" }),
		);
		await screen.findByRole("menuitem", { name: "Total logins" });
		await waitFor(() => expect(client.isFetching()).toBeGreaterThan(0));
		// Close the menu: the observer goes away while the read is in flight.
		fireEvent.keyDown(document.activeElement ?? document.body, {
			key: "Escape",
		});
		await waitFor(() => expect(screen.queryByRole("menuitem")).toBeNull());
		release();
		gate = null;
		await waitFor(() => expect(client.isFetching()).toBe(0));
		await act(settle);

		mode = 503;
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Overview" }),
		);
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "Total logins" }),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(screen.queryAllByText("37")).toHaveLength(0);
	});
});
