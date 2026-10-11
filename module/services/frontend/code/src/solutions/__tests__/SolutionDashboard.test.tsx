import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
	act,
	cleanup,
	fireEvent,
	screen,
	within,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { SolutionDashboards } from "../SolutionDashboard";

// Auth state is mutable so a test can drop the org context and assert the
// pre-org window renders as loading, not as an empty dashboard, or switch the
// viewer to show a layout belongs to one user.
const { authState } = vi.hoisted(() => ({
	authState: {
		organizationId: "org-1" as string | undefined,
		user: { id: "user-1" },
	},
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));

// Where the viewer's layout of the "activity" dashboard is kept.
const LAYOUT_KEY = "solution-dashboard:layout:example:activity:org-1:user-1";

beforeEach(() => {
	authState.organizationId = "org-1";
	authState.user = { id: "user-1" };
	window.localStorage.clear();
});
afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	window.localStorage.clear();
});

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
		// Declared but drawn by no widget: no viewer can put it on the page.
		{
			id: "logins_by_category",
			kind: "source",
			title: "Logins by category",
			filter: { event: "login" },
			groupBy: "category",
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

// A rejected refetch reaches the component a tick after `refetchQueries`
// resolves. Without this wait an assertion can run while the previous state is
// still rendered, which makes the retention tests pass whatever the code does.
const settle = () => new Promise((resolve) => setTimeout(resolve, 20));

const DEFAULT_ORDER = ["Logins over time", "Top event types", "Total logins"];

// The tile titles, in the order the dashboard shows them. The kit's renderer
// titles a series widget through its Card (`card-heading`) and a scalar one
// through its StatTile (`metric-label`), so a tile is named by whichever of
// the two it carries.
function tileTitles(): string[] {
	return screen
		.getAllByRole("listitem")
		.map(
			(tile) =>
				tile.querySelector(
					'[data-slot="card-heading"], [data-slot="metric-label"]',
				)?.textContent ?? "",
		);
}

function tile(title: string): HTMLElement {
	const found = screen
		.getAllByRole("listitem")
		.find((item) => item.textContent?.includes(title));
	if (!found) throw new Error(`no tile titled ${title}`);
	return found;
}

function renderDashboards() {
	return renderInApp(<SolutionDashboards graph={graph} solutionId="example" />);
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
					{ key: "saas.auth.login", count: "42" },
					{ key: "saas.org.created", count: "10" },
				],
			),
		);

		renderInApp(<SolutionDashboards graph={graph} solutionId="example" />);

		// Bar widget: each event_type bucket becomes a bar, and every exact
		// value is reachable in the chart's accessible data table — which is
		// what a reader who cannot see the geometry gets.
		const bars = await screen.findByRole("table", { name: "Top event types" });
		expect(
			within(bars).getByRole("row", { name: /saas\.auth\.login/ }),
		).toBeTruthy();
		expect(within(bars).getByText("42")).toBeTruthy();
		// Number widget: the total-logins stat sums the time buckets (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
		// Each widget's title is rendered from the declaration. A chart widget
		// says it three times: once visibly as its card's heading, and twice in
		// the accessible data table that is the chart's screen-reader
		// representation — as the table's caption and as its one series' column
		// header, there being no other name for the series than the widget's.
		expect(screen.getAllByText("Logins over time")).toHaveLength(3);
		expect(screen.getByText("Total logins")).toBeTruthy();
	});

	it("isolates a widget whose metric fails without blanking its siblings", async () => {
		// The bar widget's metric query errors; the time-series widgets still
		// resolve. Under one batched dashboard query this 500 would reject the
		// whole resolution and blank every widget, so the stat below could never
		// appear.
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					const body = (await request.json()) as { groupBy?: string };
					if (body.groupBy === "event_type") {
						return new HttpResponse(null, { status: 500 });
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

		renderInApp(<SolutionDashboards graph={graph} solutionId="example" />);

		// The healthy total-logins stat resolves (3 + 5)...
		expect(await screen.findByText("8")).toBeTruthy();
		// ...even though the bar widget's own query failed.
		expect(await screen.findByText("Unable to load.")).toBeTruthy();
	});

	// The sequence that matters: a read that SUCCEEDED, then one that did not.
	// TanStack Query hands back the last successful data when a refetch
	// rejects, so a cold failure cannot show this — there is nothing retained
	// to leak.
	describe.each([
		[403, "permission_denied", Code.PermissionDenied],
		[503, "unavailable", Code.Unavailable],
	] as const)("a %d on a refetch", (status, code, expected) => {
		const refused = status === 403;

		it(`${refused ? "discards" : "keeps"} the values already on screen`, async () => {
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
				),
			);
			const { client } = renderDashboards();
			expect(await screen.findAllByText("37")).not.toHaveLength(0);

			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ code, message: "read rejected" }, { status }),
				),
			);
			await act(async () => {
				await client.refetchQueries({ queryKey: ["solution-widget"] });
			});

			const query = client
				.getQueryCache()
				.find({ queryKey: ["solution-widget"], exact: false });
			expect(ConnectError.from(query?.state.error).code).toBe(expected);

			if (refused) {
				// A number this viewer is no longer allowed to read is a wrong
				// answer, not a stale one.
				expect(await screen.findAllByText("No access")).not.toHaveLength(0);
				expect(screen.queryAllByText("37")).toHaveLength(0);
				expect(screen.queryByText("Stale")).toBeNull();
			} else {
				// A transient failure says nothing about authority, so the
				// values stay and read as out of date.
				expect(await screen.findAllByText("Stale")).not.toHaveLength(0);
				expect(screen.getAllByText("37").length).toBeGreaterThan(0);
			}
		});
	});

	// The longer sequence, and the one that matters most: a refusal followed by
	// an ordinary failure, with no authorized read in between. A snapshot that
	// outlived the refusal would be eligible again at the next error, which
	// hands those values straight back.
	describe.each(["line", "area", "bar", "number", "table"] as const)(
		"a refused %s widget",
		(visualization) => {
			const oneWidget: DataGraph = {
				...graph,
				dashboards: [
					{
						...graph.dashboards[0],
						widgets: [{ ...graph.dashboards[0].widgets[0], visualization }],
					},
				],
			};

			it.each([
				[
					"a 503",
					() => HttpResponse.json({ code: "unavailable" }, { status: 503 }),
				],
				["a dropped connection", () => HttpResponse.error()],
			])("does not resurrect its values on %s", async (_name, fail) => {
				server.use(
					http.post(rpc("AuditService", "AggregateAuditLog"), () =>
						HttpResponse.json({
							buckets: [{ key: "2026-09-01", count: "37" }],
						}),
					),
				);
				const { client } = renderInApp(
					<SolutionDashboards graph={oneWidget} solutionId="example" />,
				);
				expect(await screen.findAllByText("37")).not.toHaveLength(0);

				server.use(
					http.post(rpc("AuditService", "AggregateAuditLog"), () =>
						HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
					),
				);
				await act(async () => {
					await client.refetchQueries({ queryKey: ["solution-widget"] });
					await settle();
				});
				expect(await screen.findByText("No access")).toBeTruthy();
				expect(screen.queryAllByText("37")).toHaveLength(0);

				server.use(http.post(rpc("AuditService", "AggregateAuditLog"), fail));
				await act(async () => {
					await client.refetchQueries({ queryKey: ["solution-widget"] });
					await settle();
				});
				expect(screen.queryAllByText("37")).toHaveLength(0);
			});
		},
	);

	it("does not resurrect them once the caches have had time to expire", async () => {
		// The shape this replaced marked the snapshot ineligible from a second
		// cache entry, which had its own garbage-collection clock: five
		// minutes later the mark was gone and the values were not, so a retry
		// handed them back. Deleting the snapshot leaves nothing with a
		// lifetime of its own to fall out of step.
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
			),
		);
		const { client } = renderDashboards();
		expect(await screen.findAllByText("37")).not.toHaveLength(0);

		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(await screen.findAllByText("No access")).not.toHaveLength(0);

		// Past the default five-minute gcTime, on the cache's own clock.
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		try {
			await act(async () => {
				await vi.advanceTimersByTimeAsync(300_001);
			});
		} finally {
			vi.useRealTimers();
		}

		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "unavailable" }, { status: 503 }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(screen.queryAllByText("37")).toHaveLength(0);
	});

	it("does not resurrect them on a remount", async () => {
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
			),
		);
		const { client, unmount } = renderDashboards();
		expect(await screen.findAllByText("37")).not.toHaveLength(0);

		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(await screen.findAllByText("No access")).not.toHaveLength(0);

		unmount();
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "unavailable" }, { status: 503 }),
			),
		);
		renderInApp(
			<SolutionDashboards graph={graph} solutionId="example" />,
			client,
		);
		await act(async () => {
			await settle();
		});
		expect(screen.queryAllByText("37")).toHaveLength(0);
	});

	it("does not leave the same numbers behind under another graph revision", async () => {
		// The graph is the last part of the query key, so a declaration that
		// differs only in a dashboard title has its own cache entry holding
		// the same numbers. A refusal has to reach every revision of them.
		const renamed: DataGraph = {
			...graph,
			dashboards: [{ ...graph.dashboards[0], title: "Activity (renamed)" }],
		};
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
			),
		);
		const { client, unmount } = renderDashboards();
		expect(await screen.findAllByText("37")).not.toHaveLength(0);
		unmount();

		// A second revision, cached under its own key.
		const second = renderInApp(
			<SolutionDashboards graph={renamed} solutionId="example" />,
			client,
		);
		expect(await screen.findAllByText("37")).not.toHaveLength(0);

		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
			await settle();
		});
		expect(await screen.findAllByText("No access")).not.toHaveLength(0);
		second.unmount();

		// Back to the first revision, whose refresh fails transiently.
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "unavailable" }, { status: 503 }),
			),
		);
		renderInApp(
			<SolutionDashboards graph={graph} solutionId="example" />,
			client,
		);
		await act(async () => {
			await settle();
		});
		expect(screen.queryAllByText("37")).toHaveLength(0);
	});

	it("lets a successful authorized read lift the refusal", async () => {
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
			),
		);
		const { client } = renderDashboards();
		expect(await screen.findAllByText("37")).not.toHaveLength(0);

		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ code: "permission_denied" }, { status: 403 }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
		});
		expect(await screen.findAllByText("No access")).not.toHaveLength(0);

		// Authority restored: a read succeeds and the NEW values are drawn.
		server.use(
			http.post(rpc("AuditService", "AggregateAuditLog"), () =>
				HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "42" }] }),
			),
		);
		await act(async () => {
			await client.refetchQueries({ queryKey: ["solution-widget"] });
		});
		expect(await screen.findAllByText("42")).not.toHaveLength(0);
		expect(screen.queryByText("No access")).toBeNull();
	});

	// Everything above drives the refusal through a mounted component. These
	// drive it where no mounted component can answer for it: the refusal is a
	// property of the READ, and the read outlives whoever started it.
	describe("a refusal with nobody watching it", () => {
		// A handler that holds its request open until the test releases it, so a
		// refusal can be in flight across an unmount or a change of dashboard.
		function heldRefusal() {
			let enter!: () => void;
			let release!: () => void;
			const entered = new Promise<void>((resolve) => {
				enter = resolve;
			});
			const released = new Promise<void>((resolve) => {
				release = resolve;
			});
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), async () => {
					enter();
					await released;
					return HttpResponse.json(
						{ code: "permission_denied" },
						{ status: 403 },
					);
				}),
			);
			return { entered, release };
		}

		it.each([
			[
				"a 503",
				() => HttpResponse.json({ code: "unavailable" }, { status: 503 }),
			],
			["a dropped connection", () => HttpResponse.error()],
		])(
			"deletes the values when it completes after the page is gone, then %s",
			async (_name, fail) => {
				server.use(
					http.post(rpc("AuditService", "AggregateAuditLog"), () =>
						HttpResponse.json({
							buckets: [{ key: "2026-09-01", count: "37" }],
						}),
					),
				);
				const { client, unmount } = renderDashboards();
				expect(await screen.findAllByText("37")).not.toHaveLength(0);

				const { entered, release } = heldRefusal();
				const refusal = client.refetchQueries({
					queryKey: ["solution-widget"],
				});
				await entered;
				// The viewer leaves while the request is still open, so no effect
				// of theirs is left to run when the answer comes back.
				unmount();
				release();
				await refusal;

				// A retry nobody is watching either. It must not hand the values
				// back to the next mount.
				server.use(http.post(rpc("AuditService", "AggregateAuditLog"), fail));
				await client.refetchQueries({ queryKey: ["solution-widget"] });

				renderInApp(
					<SolutionDashboards graph={graph} solutionId="example" />,
					client,
				);
				await act(async () => {
					await settle();
				});
				expect(screen.queryAllByText("37")).toHaveLength(0);
			},
		);

		it("deletes the revision on screen when it lands on the one the viewer left", async () => {
			// The refusal is for the revision the viewer has navigated away from;
			// the one mounted is another revision of the same widget, with its own
			// cached values on screen. Both are this widget's source, and nothing
			// in React connects the refused read to the component drawing them.
			const renamed: DataGraph = {
				...graph,
				dashboards: [{ ...graph.dashboards[0], title: "Activity (renamed)" }],
			};
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
				),
			);
			const { client, rerender } = renderDashboards();
			expect(await screen.findAllByText("37")).not.toHaveLength(0);

			// The second revision reads its own values, under its own key.
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "42" }] }),
				),
			);
			rerender(<SolutionDashboards graph={renamed} solutionId="example" />);
			expect(await screen.findAllByText("42")).not.toHaveLength(0);
			rerender(<SolutionDashboards graph={graph} solutionId="example" />);
			await act(async () => {
				await settle();
			});

			const { entered, release } = heldRefusal();
			const refusal = client.refetchQueries({
				queryKey: ["solution-widget"],
				type: "active",
			});
			await entered;

			// Mid-refusal the viewer switches back, where the other revision's
			// values are cached and its refresh fails transiently: 42, stale.
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ code: "unavailable" }, { status: 503 }),
				),
			);
			rerender(<SolutionDashboards graph={renamed} solutionId="example" />);
			expect(await screen.findAllByText("Stale")).not.toHaveLength(0);
			expect(screen.getAllByText("42").length).toBeGreaterThan(0);

			await act(async () => {
				release();
				await refusal;
				await settle();
			});
			expect(screen.queryAllByText("42")).toHaveLength(0);
		});

		it("asks once, rather than clearing its own error and asking again", async () => {
			// Clearing the entry to drop its values restarts every active read of
			// it, whose next answer is the same refusal: a standing 403 became an
			// unbounded loop. Deleting the values touches neither the error nor
			// the fetch, so the retry policy is the client's alone.
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), () =>
					HttpResponse.json({ buckets: [{ key: "2026-09-01", count: "37" }] }),
				),
			);
			const { client } = renderDashboards();
			expect(await screen.findAllByText("37")).not.toHaveLength(0);

			let refusals = 0;
			server.use(
				http.post(rpc("AuditService", "AggregateAuditLog"), async () => {
					refusals += 1;
					// Answered a tick late, so the component sees the pending state
					// in between — the window a self-restarting clear re-entered.
					await new Promise((resolve) => setTimeout(resolve, 15));
					return HttpResponse.json(
						{ code: "permission_denied" },
						{ status: 403 },
					);
				}),
			);
			await act(async () => {
				await client.refetchQueries({ queryKey: ["solution-widget"] });
			});
			expect(await screen.findAllByText("No access")).not.toHaveLength(0);
			for (let round = 0; round < 8; round += 1) {
				await act(async () => {
					await settle();
				});
			}
			// Three widgets, one refresh, `retry: false` in tests.
			expect(refusals).toBe(3);
		});
	});

	it("reads as loading until the org context resolves, never as empty", () => {
		authState.organizationId = undefined;

		const { container } = renderInApp(
			<SolutionDashboards graph={graph} solutionId="example" />,
		);

		// Card shells render immediately, but each body stays gated behind its
		// disabled query — a skeleton, never a resolved value or a bare "no data".
		expect(screen.getByText("Activity")).toBeTruthy();
		expect(screen.getByText("Total logins")).toBeTruthy();
		expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull();
		expect(screen.queryByText("8")).toBeNull();
	});
});

describe("where a tile's number comes from", () => {
	// Answers the audit trail the ⓘ asks, and counts what the page asks for, so
	// a test can tell the ⓘ's own requests apart from the tiles'.
	function auditTrail() {
		const asked = { aggregates: 0, searches: 0 };
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					asked.aggregates += 1;
					const body = (await request.json()) as { groupBy?: string };
					return HttpResponse.json({
						buckets:
							body.groupBy === "event_type"
								? [{ key: "saas.auth.login", count: "42" }]
								: [
										{ key: "2026-08-01T00:00:00+00", count: "3" },
										{ key: "2026-08-02T00:00:00+00", count: "5" },
									],
					});
				},
			),
			http.post(rpc("AuditService", "QueryAuditLog"), () => {
				asked.searches += 1;
				return HttpResponse.json({
					events: [
						{
							id: "evt-2",
							actorId: "person-1",
							eventType: "auth.login.v1",
							createdAt: "2026-08-02T14:32:00Z",
						},
						{
							id: "evt-1",
							actorId: "person-2",
							eventType: "auth.login.v1",
							createdAt: "2026-08-01T09:05:00Z",
						},
					],
					totalCount: 8,
				});
			}),
			http.post(rpc("PrincipalService", "ListPrincipals"), () =>
				HttpResponse.json({
					principals: [
						{
							id: "person-1",
							displayName: "Jane Doe",
							kind: "PRINCIPAL_KIND_HUMAN",
						},
					],
				}),
			),
			http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
				HttpResponse.json({
					types: [{ name: "auth.login.v1", description: "A user signed in." }],
				}),
			),
		);
		return asked;
	}
	const moment = (iso: string, year = true) =>
		`${new Date(iso).toLocaleString(undefined, {
			month: "short",
			day: "numeric",
			...(year ? { year: "numeric" } : {}),
			hour: "numeric",
			minute: "2-digit",
			timeZone: "UTC",
		})} UTC`;

	it("says what the tile counts, from which events, and when they happened", async () => {
		auditTrail();
		renderDashboards();
		fireEvent.click(screen.getByRole("button", { name: "About Total logins" }));
		const panel = await screen.findByRole("dialog");

		// A number tile shows one total, so its per-day grouping goes unsaid.
		expect(within(panel).getByText("Number of events")).toBeTruthy();
		expect(await within(panel).findByText("A user signed in.")).toBeTruthy();
		expect(within(panel).getByText("auth.login.v1")).toBeTruthy();
		expect(within(panel).getByText("Your organization")).toBeTruthy();
		expect(within(panel).getByText("All time")).toBeTruthy();
		// The per-day counts: 3 + 5 events, over two days.
		expect(await within(panel).findByText("8 events")).toBeTruthy();
		expect(within(panel).getByText("Aug 1, 2026 – Aug 2, 2026")).toBeTruthy();
		// The newest events, named where the directory knows the person.
		expect(
			await within(panel).findByText(
				`${moment("2026-08-02T14:32:00Z")}, by Jane Doe`,
			),
		).toBeTruthy();
		const recent = within(panel).getAllByRole("listitem");
		expect(recent.map((item) => item.textContent)).toEqual([
			`${moment("2026-08-02T14:32:00Z", false)} · Jane Doe`,
			`${moment("2026-08-01T09:05:00Z", false)} · Actor unavailable`,
		]);
	});

	it("asks the audit trail nothing until the viewer opens it", async () => {
		const asked = auditTrail();
		renderDashboards();
		// One query per tile, and none for the ⓘ yet.
		await screen.findByText("8");
		expect(asked).toEqual({ aggregates: 3, searches: 0 });

		fireEvent.click(
			screen.getByRole("button", { name: "About Logins over time" }),
		);
		expect(
			await within(await screen.findByRole("dialog")).findByText("8 events"),
		).toBeTruthy();
		await within(screen.getByRole("dialog")).findAllByRole("listitem");
		expect(asked).toEqual({ aggregates: 4, searches: 1 });
	});
});

describe("a viewer's layout", () => {
	beforeEach(() => {
		server.use(
			aggregateHandler(
				[
					{ key: "2026-08-01", count: "3" },
					{ key: "2026-08-02", count: "5" },
				],
				[{ key: "saas.auth.login", count: "42" }],
			),
		);
	});

	it("starts from the declared widgets in declared order", () => {
		renderDashboards();
		expect(tileTitles()).toEqual(DEFAULT_ORDER);
		expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
	});

	it("removes a tile, and it stays removed after a remount", () => {
		const { unmount } = renderDashboards();
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Top event types" }),
		);
		expect(tileTitles()).toEqual(["Logins over time", "Total logins"]);

		unmount();
		renderDashboards();
		expect(tileTitles()).toEqual(["Logins over time", "Total logins"]);
	});

	it("adds a removed widget back from the + menu, and offers no undrawn metric", async () => {
		renderDashboards();
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Top event types" }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Activity" }),
		);
		// "Logins by category" is declared in the graph but drawn by no widget:
		// a preference never adds a metric (ADR 0007).
		const items = await screen.findAllByRole("menuitem");
		expect(items.map((item) => item.textContent)).toEqual(["Top event types"]);

		fireEvent.click(screen.getByRole("menuitem", { name: "Top event types" }));
		expect(tileTitles()).toEqual([
			"Logins over time",
			"Total logins",
			"Top event types",
		]);
	});

	it("ignores a stored tile the dashboard does not declare", () => {
		window.localStorage.setItem(
			LAYOUT_KEY,
			JSON.stringify({
				version: 1,
				tiles: ["w_stat", "metric:logins_by_category", "w_line"],
				seen: ["w_line", "w_bar", "w_stat"],
			}),
		);
		renderDashboards();
		expect(tileTitles()).toEqual(["Total logins", "Logins over time"]);
		expect(screen.queryByText("Logins by category")).toBeNull();
	});

	describe("dragging", () => {
		// happy-dom lays nothing out, so each tile gets a slot by its place in
		// the list: two columns of 100×100 cells, 10px apart.
		const realRect = HTMLElement.prototype.getBoundingClientRect;
		beforeEach(() => {
			HTMLElement.prototype.getBoundingClientRect = function (
				this: HTMLElement,
			) {
				const index =
					this.dataset.sortableId === undefined || !this.parentElement
						? 0
						: [...this.parentElement.children].indexOf(this);
				const left = (index % 2) * 110;
				const top = Math.floor(index / 2) * 110;
				return {
					x: left,
					y: top,
					left,
					top,
					right: left + 100,
					bottom: top + 100,
					width: 100,
					height: 100,
					toJSON: () => ({}),
				} as DOMRect;
			};
		});
		afterEach(async () => {
			HTMLElement.prototype.getBoundingClientRect = realRect;
			// dnd-kit keeps swallowing clicks for 50ms after a drag ends.
			await new Promise((resolve) => setTimeout(resolve, 60));
		});

		const middle = (title: string) => {
			const { left, top } = tile(title).getBoundingClientRect();
			return { clientX: left + 50, clientY: top + 50 };
		};
		// A mouse drag: pressed on the tile, then moved on the document, where
		// dnd-kit listens once a drag begins. Returns the drop.
		const drag = (title: string, to: { clientX: number; clientY: number }) => {
			const from = middle(title);
			fireEvent.mouseDown(tile(title), { ...from, button: 0 });
			for (const step of [0.1, 0.5, 1]) {
				fireEvent.mouseMove(document, {
					clientX: from.clientX + (to.clientX - from.clientX) * step,
					clientY: from.clientY + (to.clientY - from.clientY) * step,
				});
			}
			return () => fireEvent.mouseUp(document, to);
		};

		it("swaps a tile dropped on another, and saves the order on the drop", () => {
			const { unmount } = renderDashboards();
			const drop = drag("Logins over time", middle("Total logins"));
			// Nothing is committed while the drag is in flight...
			expect(tileTitles()).toEqual(DEFAULT_ORDER);
			expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();

			// ...and the drop swaps only the two: the tile between them stays put.
			drop();
			const swapped = ["Total logins", "Top event types", "Logins over time"];
			expect(tileTitles()).toEqual(swapped);

			unmount();
			renderDashboards();
			expect(tileTitles()).toEqual(swapped);
		});

		it("draws the dragged copy without its info panel or reachable controls", () => {
			renderDashboards();
			const drop = drag("Logins over time", middle("Total logins"));
			// The floating copy repeats the card, but not its controls: one info
			// trigger and one remove button per tile, the tile's own.
			expect(
				document.querySelectorAll('[aria-label="About Logins over time"]'),
			).toHaveLength(1);
			expect(
				document.querySelectorAll('[aria-label="Remove Logins over time"]'),
			).toHaveLength(1);
			const copies = document.querySelectorAll("[inert]");
			expect(copies).toHaveLength(1);
			expect(copies[0].getAttribute("aria-hidden")).toBe("true");
			expect(copies[0].textContent).toContain("Logins over time");
			expect(copies[0].querySelector("[tabindex='0'], a[href]")).toBeNull();
			drop();
		});

		it("changes nothing when a drag ends off the dashboard", () => {
			renderDashboards();
			drag("Total logins", { clientX: 900, clientY: 900 })();
			expect(tileTitles()).toEqual(DEFAULT_ORDER);
			expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
		});
	});

	// happy-dom keeps focus on a node React moves, where a browser drops it, so
	// the grip taking focus back after a move is not observable here.
	it("moves a tile with the arrow keys on its grip", () => {
		renderDashboards();
		const grip = screen.getByRole("button", {
			name: "Move Logins over time: drag the tile, or use the arrow keys",
		});
		grip.focus();
		fireEvent.keyDown(grip, { key: "ArrowDown" });
		expect(tileTitles()).toEqual([
			"Top event types",
			"Logins over time",
			"Total logins",
		]);

		fireEvent.keyDown(grip, { key: "ArrowUp" });
		expect(tileTitles()).toEqual(DEFAULT_ORDER);
	});

	it("keeps each viewer's layout to themselves", () => {
		const { unmount } = renderDashboards();
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Total logins" }),
		);
		unmount();

		authState.user = { id: "user-2" };
		renderDashboards();
		expect(tileTitles()).toEqual(DEFAULT_ORDER);
	});

	it("falls back to the declared layout when the saved one is unreadable", () => {
		window.localStorage.setItem(LAYOUT_KEY, "{ not json");
		renderDashboards();
		expect(tileTitles()).toEqual(DEFAULT_ORDER);
	});

	it("falls back when storage is blocked, and still applies the viewer's change", () => {
		vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
			throw new Error("SecurityError");
		});
		vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
			throw new Error("QuotaExceededError");
		});
		renderDashboards();
		expect(tileTitles()).toEqual(DEFAULT_ORDER);

		fireEvent.click(
			screen.getByRole("button", { name: "Remove Logins over time" }),
		);
		expect(tileTitles()).toEqual(["Top event types", "Total logins"]);
	});
});
