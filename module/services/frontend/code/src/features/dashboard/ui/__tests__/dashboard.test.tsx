import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { dashboard, event, metric } from "../../model/schema";
import { Dashboard } from "../dashboard";

// Auth state is mutable so a test can drop the org context and assert the
// no-org window renders as loading, not as empty.
const { authState } = vi.hoisted(() => ({
	authState: { organizationId: "org-1" as string | undefined },
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));

beforeEach(() => {
	authState.organizationId = "org-1";
});
afterEach(cleanup);

const login = event("auth.login");
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

describe("Dashboard", () => {
	it("renders real audit aggregates for each declared metric", async () => {
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

		renderInApp(<Dashboard data={insights} />);

		// Categorical metric: humanized event label + its count.
		expect(await screen.findByText("Auth Login")).toBeTruthy();
		expect(screen.getByText("42")).toBeTruthy();
		// Stat metric: the summed total (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
	});

	it("draws a line metric with a single time bucket instead of a blank card", async () => {
		server.use(
			aggregateHandler(
				[{ key: "2026-08-01", count: "4" }],
				[{ key: "auth.login", count: "4" }],
			),
		);

		renderInApp(<Dashboard data={insights} />);

		// The line metric renders its chart (not an empty body), and the stat
		// metric shows the single bucket's value.
		expect(await screen.findByLabelText("Line chart")).toBeTruthy();
		expect(screen.getAllByText("4").length).toBeGreaterThan(0);
		expect(screen.queryByText("No events yet.")).toBeNull();
	});

	it("surfaces a failed aggregate as an error, not as empty", async () => {
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ message: "boom" }, { status: 500 }),
			),
		);

		renderInApp(<Dashboard data={insights} />);

		expect(
			await screen.findAllByText("Unable to load this metric."),
		).not.toHaveLength(0);
		expect(screen.queryByText("No events yet.")).toBeNull();
	});

	it("lays out as a grid with the spec's column count and spans a widget", () => {
		server.use(aggregateHandler([], []));

		const spec = dashboard({
			layout: { kind: "grid", columns: 3 },
			metrics: [
				metric({ title: "Wide", groupBy: "event_type", chart: "bar", span: 2 }),
				metric({ title: "Narrow", groupBy: "event_type", chart: "bar" }),
			],
		});
		const { container } = renderInApp(<Dashboard data={spec} />);

		const grid = container.querySelector('[data-slot="grid"]');
		expect(grid?.className).toContain("lg:grid-cols-3");
		expect(
			container.querySelector('[data-slot="card"].sm\\:col-span-2'),
		).toBeTruthy();
	});

	it("clamps a span wider than the grid to the column count", () => {
		server.use(aggregateHandler([], []));

		// A span of 4 in a 2-column grid must not emit col-span-4: CSS grid would
		// spawn implicit columns and break the row. It clamps to 2 instead.
		const spec = dashboard({
			layout: { kind: "grid", columns: 2 },
			metrics: [
				metric({ title: "Wide", groupBy: "event_type", chart: "bar", span: 4 }),
			],
		});
		const { container } = renderInApp(<Dashboard data={spec} />);

		const card = container.querySelector('[data-slot="card"]');
		expect(card?.className).toContain("sm:col-span-2");
		expect(card?.className).not.toContain("col-span-3");
		expect(card?.className).not.toContain("col-span-4");
	});

	it("lays out as a stack when the spec asks for one", () => {
		server.use(aggregateHandler([], []));

		const spec = dashboard({
			layout: { kind: "stack" },
			metrics: [metric({ title: "Only", groupBy: "event_type", chart: "bar" })],
		});
		const { container } = renderInApp(<Dashboard data={spec} />);

		expect(container.querySelector('[data-slot="stack"]')).toBeTruthy();
		expect(container.querySelector('[data-slot="grid"]')).toBeNull();
	});

	it("applies the spec's accent as a primary override the charts inherit", async () => {
		server.use(aggregateHandler([], [{ key: "auth.login", count: "5" }]));

		const spec = dashboard({
			theme: { accent: "oklch(0.6 0.2 20)" },
			metrics: [metric({ title: "Only", groupBy: "event_type", chart: "bar" })],
		});
		const { container } = renderInApp(<Dashboard data={spec} />);

		const root = container.firstChild as HTMLElement;
		expect(root.style.getPropertyValue("--primary")).toBe("oklch(0.6 0.2 20)");

		// The override only means anything if the chart actually colors from the
		// primary token: assert a primary-keyed chart element renders inside the
		// accented subtree, so hardcoding a color in a chart would fail here.
		await screen.findByText("Auth Login");
		expect(root.querySelector(".bg-primary\\/70")).toBeTruthy();
	});

	it("plots a percentile metric's value from the bucket metrics map", async () => {
		let sentOp: string | undefined;
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					const body = (await request.json()) as {
						metrics?: Array<{ op: string }>;
					};
					sentOp = body.metrics?.[0]?.op;
					return HttpResponse.json({
						buckets: [
							{
								key: "2026-08-01",
								keys: ["2026-08-01"],
								count: "128",
								metrics: { value: 4200 },
							},
						],
					});
				},
			),
		);

		const latency = dashboard({
			metrics: [
				metric({
					title: "p95 request latency",
					event: event("http.request_served"),
					groupBy: "time",
					bucket: "day",
					chart: "stat",
					value: {
						op: "percentile",
						field: "payload:duration_ms",
						percentile: 0.95,
					},
				}),
			],
		});

		renderInApp(<Dashboard data={latency} />);

		// The card shows the aliased percentile (4,200), not the group's row
		// count (128), and the widened query reached the RPC as a percentile op.
		expect(await screen.findByText("4,200")).toBeTruthy();
		expect(screen.queryByText("128")).toBeNull();
		expect(sentOp).toBe("percentile");
	});

	it("averages a non-additive stat across buckets instead of summing them", async () => {
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({
					buckets: [
						{
							key: "2026-08-01",
							keys: ["2026-08-01"],
							count: "10",
							metrics: { value: 4000 },
						},
						{
							key: "2026-08-02",
							keys: ["2026-08-02"],
							count: "10",
							metrics: { value: 5000 },
						},
					],
				}),
			),
		);

		const latency = dashboard({
			metrics: [
				metric({
					title: "p95 request latency",
					event: event("http.request_served"),
					groupBy: "time",
					bucket: "day",
					chart: "stat",
					value: {
						op: "percentile",
						field: "payload:duration_ms",
						percentile: 0.95,
					},
				}),
			],
		});

		renderInApp(<Dashboard data={latency} />);

		// Mean of the two daily p95s (4,500) — not their sum (9,000), which is a
		// meaningless magnitude for a percentile.
		expect(await screen.findByText("4,500")).toBeTruthy();
		expect(screen.queryByText("9,000")).toBeNull();
	});

	it("drops a bucket whose aggregate the RPC omitted rather than plotting zero", async () => {
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({
					buckets: [
						{
							key: "2026-08-01",
							keys: ["2026-08-01"],
							count: "10",
							metrics: { value: 4200 },
						},
						// Group has events but no numeric payload → alias omitted.
						{
							key: "2026-08-02",
							keys: ["2026-08-02"],
							count: "7",
							metrics: {},
						},
					],
				}),
			),
		);

		const latency = dashboard({
			metrics: [
				metric({
					title: "p95 request latency",
					event: event("http.request_served"),
					groupBy: "time",
					bucket: "day",
					chart: "stat",
					value: {
						op: "percentile",
						field: "payload:duration_ms",
						percentile: 0.95,
					},
				}),
			],
		});

		renderInApp(<Dashboard data={latency} />);

		// Only the day with data contributes; the omitted day is not a phantom 0,
		// so the mean stays 4,200 rather than collapsing to 2,100.
		expect(await screen.findByText("4,200")).toBeTruthy();
		expect(screen.queryByText("2,100")).toBeNull();
	});

	it("shows loading, not empty, before an org is resolved", () => {
		authState.organizationId = undefined;
		let called = false;
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () => {
				called = true;
				return HttpResponse.json({ buckets: [] });
			}),
		);

		const { container } = renderInApp(<Dashboard data={insights} />);

		// The query is disabled without an org, so no RPC fires and the card
		// stays in a loading skeleton rather than asserting "no events".
		expect(called).toBe(false);
		expect(screen.queryByText("No events yet.")).toBeNull();
		expect(container.querySelector('[data-slot="skeleton"]')).toBeTruthy();
	});
});
