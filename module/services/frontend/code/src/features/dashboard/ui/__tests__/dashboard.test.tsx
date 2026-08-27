import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { dashboard, event, metric } from "../../model/schema";
import { Dashboard } from "../dashboard";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1" }),
}));

afterEach(cleanup);

const login = event("auth.login", "Logins");
const insights = dashboard({
	metrics: [
		metric({
			title: "Logins over time",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "line",
		}),
		metric({
			title: "Top event types",
			groupBy: "event_type",
			chart: "bar",
			limit: 6,
		}),
		metric({
			title: "Total logins",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "stat",
		}),
	],
});

describe("Dashboard", () => {
	it("renders real audit aggregates for each declared metric", async () => {
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					const body = (await request.json()) as { groupBy?: string };
					if (body.groupBy === "event_type") {
						return HttpResponse.json({
							buckets: [
								{ key: "auth.login", count: "42" },
								{ key: "org.created", count: "10" },
							],
						});
					}
					return HttpResponse.json({
						buckets: [
							{ key: "2026-08-01", count: "3" },
							{ key: "2026-08-02", count: "5" },
						],
					});
				},
			),
		);

		renderInApp(<Dashboard data={insights} />);

		// Categorical metric: humanized event label + its count.
		expect(await screen.findByText("Auth Login")).toBeTruthy();
		expect(screen.getByText("42")).toBeTruthy();
		// Stat metric: the summed total (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
	});
});
