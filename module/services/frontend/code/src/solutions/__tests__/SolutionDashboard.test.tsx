import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { SolutionDashboards } from "../SolutionDashboard";

// Auth state is mutable so a test can drop the org context and assert the
// pre-org window renders as loading, not as an empty dashboard.
const { authState } = vi.hoisted(() => ({
	authState: { organizationId: "org-1" as string | undefined },
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));

beforeEach(() => {
	authState.organizationId = "org-1";
});
afterEach(cleanup);

// The acceptance data graph: a registered solution ships only this declaration —
// logins-over-time line, top-event-types bar, total-logins stat — and no
// charting code. The host resolves it against the audit trail and renders it.
const graph: DataGraph = {
	events: [{ name: "login", type: "auth.login.v1" }],
	metrics: [
		{
			id: "logins_over_time",
			kind: "source",
			filter: { event: "login" },
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		},
		{
			id: "event_types",
			kind: "source",
			filter: { event: "login" },
			groupBy: "event_type",
			aggregation: "count",
		},
		{
			id: "total_logins",
			kind: "source",
			filter: { event: "login" },
			groupBy: "time",
			bucket: "day",
			aggregation: "count",
		},
	],
	dashboards: [
		{
			id: "activity",
			title: "Activity",
			layout: "grid",
			widgets: [
				{
					id: "w_line",
					metric: "logins_over_time",
					visualization: "line",
					title: "Logins over time",
				},
				{
					id: "w_bar",
					metric: "event_types",
					visualization: "bar",
					title: "Top event types",
				},
				{
					id: "w_stat",
					metric: "total_logins",
					visualization: "number",
					title: "Total logins",
				},
			],
		},
	],
};

function aggregateHandler(
	timeBuckets: Array<{ key: string; count: string }>,
	typeBuckets: Array<{ key: string; count: string }>,
) {
	return http.post(
		rpc("AuditService", "AggregateAuditLog"),
		async ({ request }) => {
			const body = (await request.json()) as { groupBy?: string };
			return HttpResponse.json({
				buckets: body.groupBy === "event_type" ? typeBuckets : timeBuckets,
			});
		},
	);
}

describe("SolutionDashboards", () => {
	it("resolves a solution's data graph against the audit trail and renders it", async () => {
		server.use(
			aggregateHandler(
				[
					{ key: "2026-08-01", count: "3" },
					{ key: "2026-08-02", count: "5" },
				],
				[
					{ key: "auth.login", count: "42" },
					{ key: "org.created", count: "10" },
				],
			),
		);

		renderInApp(<SolutionDashboards graph={graph} solutionId="lastlogin" />);

		// Bar widget: each event_type bucket becomes a labelled bar.
		expect(await screen.findByText("auth.login")).toBeTruthy();
		expect(screen.getByText("42")).toBeTruthy();
		// Number widget: the total-logins stat sums the time buckets (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
		// Each widget's title is rendered from the declaration.
		expect(screen.getByText("Logins over time")).toBeTruthy();
		expect(screen.getByText("Total logins")).toBeTruthy();
	});

	it("reads as loading until the org context resolves, never as empty", () => {
		authState.organizationId = undefined;

		renderInApp(<SolutionDashboards graph={graph} solutionId="lastlogin" />);

		// The dashboard title renders immediately, but the widgets stay gated
		// behind the disabled query — so no widget resolves to a bare "no data".
		expect(screen.getByText("Activity")).toBeTruthy();
		expect(screen.queryByText("Total logins")).toBeNull();
	});
});
