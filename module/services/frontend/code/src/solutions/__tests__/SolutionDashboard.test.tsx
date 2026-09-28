import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
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

const DEFAULT_ORDER = ["Logins over time", "Top event types", "Total logins"];

// The tile titles, in the order the dashboard shows them.
function tileTitles(): string[] {
	return screen
		.getAllByRole("listitem")
		.map(
			(tile) =>
				tile.querySelector('[data-slot="card-title"]')?.textContent ?? "",
		);
}

function tile(title: string): HTMLElement {
	const found = screen
		.getAllByRole("listitem")
		.find((item) => item.textContent?.includes(title));
	if (!found) throw new Error(`no tile titled ${title}`);
	return found;
}

function renderDashboards(declared: DataGraph = graph) {
	return renderInApp(
		<SolutionDashboards graph={declared} solutionId="example" />,
	);
}

// A mouse drag on the page, pressed on a tile and moved on the document, where
// dnd-kit listens once a drag begins. Past the 5px a drag needs, it is in
// flight; `moveTo` aims it (at a slot the drag brought up, too), `drop` ends it.
function pickUp(element: HTMLElement) {
	const { left, top } = element.getBoundingClientRect();
	let at = { clientX: left + 50, clientY: top + 50 };
	fireEvent.mouseDown(element, { ...at, button: 0 });
	fireEvent.mouseMove(document, {
		clientX: at.clientX + 10,
		clientY: at.clientY + 10,
	});
	return {
		moveTo(target: HTMLElement) {
			const rect = target.getBoundingClientRect();
			const to = { clientX: rect.left + 50, clientY: rect.top + 50 };
			fireEvent.mouseMove(document, {
				clientX: (at.clientX + to.clientX) / 2,
				clientY: (at.clientY + to.clientY) / 2,
			});
			fireEvent.mouseMove(document, to);
			at = to;
		},
		drop: () => fireEvent.mouseUp(document, at),
	};
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

		// Bar widget: each event_type bucket becomes a labelled bar.
		expect(await screen.findByText("saas.auth.login")).toBeTruthy();
		expect(screen.getByText("42")).toBeTruthy();
		// Number widget: the total-logins stat sums the time buckets (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
		// Each widget's title is rendered from the declaration.
		expect(screen.getByText("Logins over time")).toBeTruthy();
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

	it("reads the version-1 layout saved before sections, and saves version 2 on the next change", () => {
		window.localStorage.setItem(
			LAYOUT_KEY,
			JSON.stringify({
				version: 1,
				tiles: ["w_stat", "w_line"],
				seen: ["w_line", "w_bar", "w_stat"],
			}),
		);
		renderDashboards();
		// w_bar was removed then, and stays removed.
		expect(tileTitles()).toEqual(["Total logins", "Logins over time"]);
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Logins over time" }),
		);
		expect(
			JSON.parse(window.localStorage.getItem(LAYOUT_KEY) ?? "null"),
		).toEqual({
			version: 2,
			sections: [{ id: "", tiles: ["w_stat"] }],
			seen: ["w_line", "w_bar", "w_stat"],
			seenSections: [""],
		});
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

		it("draws a dashboard without sections as one untitled group, with one + and no slot", () => {
			renderDashboards();
			expect(screen.queryAllByRole("heading", { level: 3 })).toEqual([]);
			expect(
				screen.getAllByRole("button", { name: /^Add a widget back to / }),
			).toEqual([
				screen.getByRole("button", { name: "Add a widget back to Activity" }),
			]);
			const drop = drag("Logins over time", middle("Total logins"));
			expect(screen.queryByText("Move here")).toBeNull();
			drop();
			expect(tileTitles()).toEqual([
				"Total logins",
				"Top event types",
				"Logins over time",
			]);
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

describe("what a + menu says about a removed widget", () => {
	type Buckets = Array<{ key: string; count: string }>;
	const DAYS: Buckets = [
		{ key: "2026-08-01", count: "3" },
		{ key: "2026-08-02", count: "5" },
	];

	// Answers each widget's query with the rows `rows` gives for its request,
	// or a 500 for "fail", and counts the queries by what they group by.
	function auditTrail(
		rows: (request: {
			groupBy?: string;
			eventType?: string;
		}) => Buckets | "fail" | Promise<Buckets>,
	) {
		const asked: Record<string, number> = {};
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					const body = (await request.json()) as {
						groupBy?: string;
						eventType?: string;
					};
					const groupBy = body.groupBy ?? "";
					asked[groupBy] = (asked[groupBy] ?? 0) + 1;
					const buckets = await rows(body);
					return buckets === "fail"
						? new HttpResponse(null, { status: 500 })
						: HttpResponse.json({ buckets });
				},
			),
		);
		return asked;
	}

	// A layout the viewer saved with only these tiles shown: the others were
	// removed before this page drew them, so nothing has asked about them yet.
	function showOnly(tiles: string[], dashboard = graph.dashboards[0]) {
		window.localStorage.setItem(
			LAYOUT_KEY,
			JSON.stringify({
				version: 2,
				sections: [{ id: "", tiles }],
				seen: dashboard.widgets.map((widget) => widget.id),
				seenSections: [""],
			}),
		);
	}

	// The page, with the query client in reach, so a test can wait until
	// every answer is in.
	function renderWatched() {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={client}>
				<SolutionDashboards graph={graph} solutionId="example" />
			</QueryClientProvider>,
		);
		return client;
	}

	const openMenu = () =>
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Activity" }),
		);

	// An item that says nothing beside its title.
	const expectBare = (item: HTMLElement, title: string) => {
		expect(item.textContent).toBe(title);
		expect(item.hasAttribute("aria-describedby")).toBe(false);
	};

	it("says No data yet. beside a removed widget whose metric returned no rows, and it can still be added", async () => {
		auditTrail(({ groupBy }) => (groupBy === "event_type" ? [] : DAYS));
		showOnly(["w_line", "w_stat"]);
		renderDashboards();
		openMenu();
		const item = await screen.findByRole("menuitem", {
			name: "Top event types",
			description: "No data yet.",
		});
		expect(item.textContent).toBe("Top event typesNo data yet.");

		// The tile it adds back draws at once, from the answer the menu got.
		fireEvent.click(item);
		const added = tile("Top event types");
		expect(within(added).getByText("No data yet.")).toBeTruthy();
		expect(added.querySelector('[data-slot="skeleton"]')).toBeNull();
	});

	it("says telemetry is incomplete beside a removed derived widget missing an input", async () => {
		const derived: DataGraph = {
			events: [
				{ name: "login", type: "auth.login.v1" },
				{ name: "logout", type: "auth.logout.v1" },
			],
			metrics: [
				{
					id: "logins",
					kind: "source",
					filter: { event: "login" },
					groupBy: "time",
					bucket: "day",
					aggregation: "count",
				},
				{
					id: "logouts",
					kind: "source",
					filter: { event: "logout" },
					groupBy: "time",
					bucket: "day",
					aggregation: "count",
				},
				{
					id: "net_logins",
					kind: "derived",
					operation: "difference",
					inputs: ["logins", "logouts"],
				},
			],
			dashboards: [
				{
					id: "activity",
					title: "Activity",
					layout: "grid",
					widgets: [
						{
							id: "w_logins",
							metric: "logins",
							visualization: "line",
							title: "Logins",
						},
						{
							id: "w_net",
							metric: "net_logins",
							visualization: "number",
							title: "Net logins",
						},
					],
				},
			],
		};
		// Logins were recorded and no logout ever was, so every day lacks one
		// of the two inputs.
		auditTrail(({ eventType }) => (eventType === "auth.logout.v1" ? [] : DAYS));
		showOnly(["w_logins"], derived.dashboards[0]);
		renderDashboards(derived);
		openMenu();
		const item = await screen.findByRole("menuitem", {
			name: "Net logins",
			description: "Telemetry incomplete.",
		});

		// The tile says the same, in its own words.
		fireEvent.click(item);
		expect(
			within(tile("Net logins")).getByText(
				"Telemetry unavailable or incomplete.",
			),
		).toBeTruthy();
	});

	it("says nothing beside a removed widget that has data", async () => {
		auditTrail(({ groupBy }) =>
			groupBy === "event_type" ? [{ key: "saas.auth.login", count: "42" }] : [],
		);
		showOnly(["w_stat"]);
		const client = renderWatched();
		openMenu();
		// The removed widget with no rows says so: the menu has its answers.
		await screen.findByRole("menuitem", {
			name: "Logins over time",
			description: "No data yet.",
		});
		await waitFor(() => expect(client.isFetching()).toBe(0));
		expectBare(
			screen.getByRole("menuitem", { name: "Top event types" }),
			"Top event types",
		);
	});

	it("says nothing while the answer is on its way, and draws no spinner", async () => {
		let answer: (rows: Buckets) => void = () => {};
		const pending = new Promise<Buckets>((resolve) => {
			answer = resolve;
		});
		const asked = auditTrail(({ groupBy }) =>
			groupBy === "event_type" ? pending : DAYS,
		);
		showOnly(["w_line", "w_stat"]);
		renderDashboards();
		openMenu();
		await waitFor(() => expect(asked.event_type).toBe(1));
		expectBare(
			screen.getByRole("menuitem", { name: "Top event types" }),
			"Top event types",
		);
		expect(
			screen
				.getByRole("menu")
				.querySelector(
					'[data-slot="skeleton"], [aria-busy], [role="progressbar"], [role="status"]',
				),
		).toBeNull();

		// Once the answer is in, the item says what it is.
		await act(async () => answer([]));
		expect(
			await screen.findByRole("menuitem", {
				name: "Top event types",
				description: "No data yet.",
			}),
		).toBeTruthy();
	});

	it("says nothing when a removed widget's query fails, rather than no data", async () => {
		auditTrail(({ groupBy }) => (groupBy === "event_type" ? "fail" : []));
		showOnly(["w_stat"]);
		const client = renderWatched();
		openMenu();
		await screen.findByRole("menuitem", {
			name: "Logins over time",
			description: "No data yet.",
		});
		await waitFor(() => expect(client.isFetching()).toBe(0));
		expect(
			client
				.getQueryCache()
				.getAll()
				.filter((query) => query.state.status === "error"),
		).toHaveLength(1);
		expectBare(
			screen.getByRole("menuitem", { name: "Top event types" }),
			"Top event types",
		);
	});

	it("asks nothing about a removed widget until a menu opens", async () => {
		const asked = auditTrail(({ groupBy }) =>
			groupBy === "event_type"
				? [{ key: "saas.auth.login", count: "42" }]
				: DAYS,
		);
		showOnly(["w_line", "w_stat"]);
		renderDashboards();
		// Both drawn tiles have their answers; the removed one was not asked.
		await screen.findByText("8");
		await waitFor(() => expect(asked).toEqual({ time: 2 }));

		openMenu();
		await waitFor(() => expect(asked).toEqual({ time: 2, event_type: 1 }));
	});
});

describe("a dashboard in sections", () => {
	// The acceptance graph with its dashboard split into an overview band (the
	// total) above a trends band (the two charts).
	const sectioned: DataGraph = {
		...graph,
		dashboards: [
			{
				...graph.dashboards[0],
				sections: [
					{
						id: "overview",
						title: "Overview",
						description:
							"The headline read: how much the application was used.",
					},
					{ id: "trends", title: "Trends" },
				],
				widgets: graph.dashboards[0].widgets.map((widget) => ({
					...widget,
					section: widget.id === "w_stat" ? "overview" : "trends",
				})),
			},
		],
	};

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

	// The heading of each section, in the order they are drawn.
	const sectionTitles = () =>
		screen
			.queryAllByRole("heading", { level: 3 })
			.map((heading) => heading.textContent);
	const section = (title: string) => {
		const found = screen
			.getByRole("heading", { level: 3, name: title })
			.closest<HTMLElement>("[data-sortable-group]");
		if (!found) throw new Error(`no section titled ${title}`);
		return found;
	};
	// The tile titles in one section, in order.
	const tilesIn = (title: string) =>
		[...section(title).querySelectorAll("[data-sortable-id]")].map(
			(item) =>
				item.querySelector('[data-slot="card-title"]')?.textContent ?? "",
		);
	const arrangement = () =>
		Object.fromEntries(sectionTitles().map((title) => [title, tilesIn(title)]));
	const saved = () =>
		JSON.parse(window.localStorage.getItem(LAYOUT_KEY) ?? "null")?.sections;

	it("draws each section in declared order, under its title and description", () => {
		renderDashboards(sectioned);
		expect(sectionTitles()).toEqual(["Overview", "Trends"]);
		expect(
			within(section("Overview")).getByText(
				"The headline read: how much the application was used.",
			),
		).toBeTruthy();
		expect(arrangement()).toEqual({
			Overview: ["Total logins"],
			Trends: ["Logins over time", "Top event types"],
		});
		// Each section has its own +; the dashboard's header has none.
		expect(
			screen
				.getAllByRole("button", { name: /^Add a widget back to / })
				.map((button) => button.getAttribute("aria-label")),
		).toEqual(["Add a widget back to Overview", "Add a widget back to Trends"]);
	});

	it("puts a removed widget back from a section's + into that section, and offers no undrawn metric", async () => {
		renderDashboards(sectioned);
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Top event types" }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Overview" }),
		);
		// "Logins by category" is declared in the graph but drawn by no widget:
		// no section's + offers it (ADR 0007).
		const items = await screen.findAllByRole("menuitem");
		expect(items.map((item) => item.textContent)).toEqual(["Top event types"]);
		fireEvent.click(screen.getByRole("menuitem", { name: "Top event types" }));
		expect(arrangement()).toEqual({
			Overview: ["Total logins", "Top event types"],
			Trends: ["Logins over time"],
		});
		expect(saved()).toEqual([
			{ id: "overview", tiles: ["w_stat", "w_bar"] },
			{ id: "trends", tiles: ["w_line"] },
		]);
		// Every widget is back, so another section's + has nothing to offer.
		fireEvent.click(
			screen.getByRole("button", { name: "Add a widget back to Trends" }),
		);
		const none = await screen.findByRole("menuitem", {
			name: "Every widget is on this dashboard",
		});
		expect(none.getAttribute("aria-disabled")).toBe("true");
	});

	it("says a section is empty, and moves a tile across sections with the arrow keys", () => {
		renderDashboards(sectioned);
		const grip = () =>
			screen.getByRole("button", {
				name: "Move Total logins: drag the tile, or use the arrow keys",
			});
		grip().focus();
		// The only tile of the first section moves down into the next, first.
		fireEvent.keyDown(grip(), { key: "ArrowDown" });
		expect(arrangement()).toEqual({
			Overview: [],
			Trends: ["Total logins", "Logins over time", "Top event types"],
		});
		expect(
			within(section("Overview")).getByText(/No widgets in this section/),
		).toBeTruthy();
		// The tile is drawn anew in its new section; its grip keeps the focus.
		expect(document.activeElement).toBe(grip());

		fireEvent.keyDown(grip(), { key: "ArrowUp" });
		expect(arrangement()).toEqual({
			Overview: ["Total logins"],
			Trends: ["Logins over time", "Top event types"],
		});
		expect(document.activeElement).toBe(grip());
	});

	it("reads a layout saved before sections, each tile in its declared section", () => {
		window.localStorage.setItem(
			LAYOUT_KEY,
			JSON.stringify({
				version: 1,
				tiles: ["w_bar", "w_stat", "metric:logins_by_category", "w_line"],
				seen: ["w_line", "w_bar", "w_stat"],
			}),
		);
		renderDashboards(sectioned);
		// A tile the dashboard does not draw is dropped, whatever it was.
		expect(arrangement()).toEqual({
			Overview: ["Total logins"],
			Trends: ["Top event types", "Logins over time"],
		});
	});

	it("appends a widget the solution declared after the save to its section", () => {
		window.localStorage.setItem(
			LAYOUT_KEY,
			JSON.stringify({
				version: 2,
				sections: [
					{ id: "overview", tiles: ["w_stat"] },
					{ id: "trends", tiles: ["w_line"] },
				],
				seen: ["w_line", "w_stat"],
				seenSections: ["overview", "trends"],
			}),
		);
		renderDashboards(sectioned);
		expect(arrangement()).toEqual({
			Overview: ["Total logins"],
			Trends: ["Logins over time", "Top event types"],
		});
	});

	describe("dragging", () => {
		// happy-dom lays nothing out, so each tile (and each slot) gets a cell by
		// its place in its section: two columns of 100×100 cells, 10px apart,
		// each section a band 1000px below the one before.
		const realRect = HTMLElement.prototype.getBoundingClientRect;
		beforeEach(() => {
			HTMLElement.prototype.getBoundingClientRect = function (
				this: HTMLElement,
			) {
				const list = this.parentElement;
				const group = list?.parentElement;
				const placed =
					this.tagName === "LI" && group?.dataset.sortableGroup !== undefined;
				const index = placed ? [...(list?.children ?? [])].indexOf(this) : 0;
				const band =
					placed && group?.parentElement
						? [...group.parentElement.children].indexOf(group)
						: 0;
				const left = (index % 2) * 110;
				const top = Math.floor(index / 2) * 110 + band * 1000;
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

		it("swaps a tile dropped on a tile in another section, and saves both sections", () => {
			const { unmount } = renderDashboards(sectioned);
			const drag = pickUp(tile("Total logins"));
			drag.moveTo(tile("Top event types"));
			expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
			drag.drop();
			const swapped = {
				Overview: ["Top event types"],
				Trends: ["Logins over time", "Total logins"],
			};
			expect(arrangement()).toEqual(swapped);
			expect(saved()).toEqual([
				{ id: "overview", tiles: ["w_bar"] },
				{ id: "trends", tiles: ["w_line", "w_stat"] },
			]);

			unmount();
			renderDashboards(sectioned);
			expect(arrangement()).toEqual(swapped);
		});

		it("moves a tile dropped on a section's slot to the end of that section, and saves it", () => {
			const { unmount } = renderDashboards(sectioned);
			expect(screen.queryByText("Move here")).toBeNull();
			const drag = pickUp(tile("Logins over time"));
			// Every section ends with a slot while the drag is in flight.
			expect(within(section("Overview")).getByText("Move here")).toBeTruthy();
			expect(within(section("Trends")).getByText("Move here")).toBeTruthy();
			drag.moveTo(within(section("Overview")).getByText("Move here"));
			drag.drop();
			expect(screen.queryByText("Move here")).toBeNull();
			const moved = {
				Overview: ["Total logins", "Logins over time"],
				Trends: ["Top event types"],
			};
			expect(arrangement()).toEqual(moved);
			expect(saved()).toEqual([
				{ id: "overview", tiles: ["w_stat", "w_line"] },
				{ id: "trends", tiles: ["w_bar"] },
			]);

			unmount();
			renderDashboards(sectioned);
			expect(arrangement()).toEqual(moved);
		});
	});

	describe("a viewer's own sections", () => {
		const options = (title: string) =>
			screen.getByRole("button", { name: `Section options for ${title}` });
		const choose = async (title: string, item: string) => {
			fireEvent.click(options(title));
			fireEvent.click(await screen.findByRole("menuitem", { name: item }));
		};
		const titleField = () =>
			screen.getByRole<HTMLInputElement>("textbox", { name: "Section title" });
		const type = (value: string) =>
			fireEvent.change(titleField(), { target: { value } });

		it("renames a section from its options: Enter keeps the title, the description stays", async () => {
			const { unmount } = renderDashboards(sectioned);
			await choose("Overview", "Rename section");
			// The field holds the title, focused and selected, and keeps the
			// focus once the menu has closed and settled: a menu hands the focus
			// back to its trigger a moment after it is gone.
			await vi.waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
			await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
			const field = titleField();
			expect(field.value).toBe("Overview");
			expect(document.activeElement).toBe(field);
			expect([field.selectionStart, field.selectionEnd]).toEqual([0, 8]);

			type("  Headlines  ");
			fireEvent.keyDown(titleField(), { key: "Enter" });
			expect(sectionTitles()).toEqual(["Headlines", "Trends"]);
			expect(
				within(section("Headlines")).getByText(
					"The headline read: how much the application was used.",
				),
			).toBeTruthy();
			expect(screen.getByText("Renamed Overview to Headlines.")).toBeTruthy();
			expect(document.activeElement).toBe(options("Headlines"));
			expect(saved()[0]).toEqual({
				id: "overview",
				title: "Headlines",
				tiles: ["w_stat"],
			});

			unmount();
			renderDashboards(sectioned);
			expect(sectionTitles()).toEqual(["Headlines", "Trends"]);
		});

		it("renames a section from its title, which starts no drag", () => {
			renderDashboards(sectioned);
			const title = screen.getByRole("button", {
				name: "Rename section Trends",
			});
			// The heading is named by the title alone.
			expect(title.closest("h3")).toBe(section("Trends").querySelector("h3"));
			const { left, top } = title.getBoundingClientRect();
			fireEvent.mouseDown(title, { clientX: left, clientY: top, button: 0 });
			fireEvent.mouseMove(document, { clientX: left + 40, clientY: top + 40 });
			fireEvent.mouseUp(document, { clientX: left + 40, clientY: top + 40 });
			expect(section("Trends").dataset.dragging).toBeUndefined();

			fireEvent.click(title);
			const field = titleField();
			expect(field.value).toBe("Trends");
			expect(document.activeElement).toBe(field);
			type("Changes");
			fireEvent.keyDown(titleField(), { key: "Enter" });
			expect(sectionTitles()).toEqual(["Overview", "Changes"]);
			expect(document.activeElement).toBe(
				screen.getByRole("button", { name: "Rename section Changes" }),
			);
		});

		it("keeps the title on Escape, or when the field is left blank", async () => {
			renderDashboards(sectioned);
			await choose("Trends", "Rename section");
			type("Changes");
			fireEvent.keyDown(titleField(), { key: "Escape" });
			expect(sectionTitles()).toEqual(["Overview", "Trends"]);
			expect(document.activeElement).toBe(options("Trends"));

			await choose("Trends", "Rename section");
			type("   ");
			fireEvent.keyDown(titleField(), { key: "Enter" });
			expect(sectionTitles()).toEqual(["Overview", "Trends"]);
			// Nothing changed, so nothing was saved.
			expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
		});

		it("keeps what was typed when the viewer leaves the field", async () => {
			renderDashboards(sectioned);
			await choose("Trends", "Rename section");
			type("Changes");
			fireEvent.blur(titleField());
			expect(sectionTitles()).toEqual(["Overview", "Changes"]);
		});

		it("adds a section after the last, straight into renaming it, and keeps it", async () => {
			const { unmount } = renderDashboards(sectioned);
			fireEvent.click(screen.getByRole("button", { name: "Add section" }));
			expect(screen.getByText("Added a section at the end.")).toBeTruthy();
			const field = titleField();
			expect(field.value).toBe("New section");
			expect(document.activeElement).toBe(field);
			type("Watch list");
			fireEvent.keyDown(titleField(), { key: "Enter" });

			expect(arrangement()).toEqual({
				Overview: ["Total logins"],
				Trends: ["Logins over time", "Top event types"],
				"Watch list": [],
			});
			// A section of the viewer's own says what it is for only when empty.
			expect(
				within(section("Watch list")).getByText(/No widgets in this section/),
			).toBeTruthy();
			expect(saved()[2]).toMatchObject({ title: "Watch list", tiles: [] });
			expect(saved()[2].id).toMatch(/^custom:[0-9a-f]{16}$/);

			// It adds nothing: its + offers only a widget the viewer removed.
			fireEvent.click(
				screen.getByRole("button", { name: "Remove Top event types" }),
			);
			fireEvent.click(
				screen.getByRole("button", { name: "Add a widget back to Watch list" }),
			);
			const items = await screen.findAllByRole("menuitem");
			expect(items.map((item) => item.textContent)).toEqual([
				"Top event types",
			]);
			fireEvent.click(
				screen.getByRole("menuitem", { name: "Top event types" }),
			);
			unmount();
			renderDashboards(sectioned);
			expect(arrangement()).toEqual({
				Overview: ["Total logins"],
				Trends: ["Logins over time"],
				"Watch list": ["Top event types"],
			});
		});

		it("removes a section, moving its tiles to the section beside it, and keeps it removed", async () => {
			const { unmount } = renderDashboards(sectioned);
			// The first section's tiles go to the start of the one below.
			await choose("Overview", "Remove section");
			expect(arrangement()).toEqual({
				Trends: ["Total logins", "Logins over time", "Top event types"],
			});
			expect(
				screen.getByText("Removed Overview; its widgets moved to Trends."),
			).toBeTruthy();
			await vi.waitFor(() =>
				expect(document.activeElement).toBe(options("Trends")),
			);

			// The last section left cannot be removed.
			fireEvent.click(options("Trends"));
			const item = await screen.findByRole("menuitem", {
				name: "Remove section",
			});
			expect(item.getAttribute("aria-disabled")).toBe("true");

			unmount();
			renderDashboards(sectioned);
			expect(arrangement()).toEqual({
				Trends: ["Total logins", "Logins over time", "Top event types"],
			});
		});

		it("moves a section with the arrow keys on its grip, and keeps the order", () => {
			const { unmount } = renderDashboards(sectioned);
			const grip = () =>
				screen.getByRole("button", {
					name: "Move Trends: drag the section, or use the arrow keys",
				});
			grip().focus();
			fireEvent.keyDown(grip(), { key: "ArrowUp" });
			expect(sectionTitles()).toEqual(["Trends", "Overview"]);
			expect(arrangement()).toEqual({
				Trends: ["Logins over time", "Top event types"],
				Overview: ["Total logins"],
			});
			expect(screen.getByText("Trends moved above Overview.")).toBeTruthy();
			expect(document.activeElement).toBe(grip());
			// Already first: it stays.
			fireEvent.keyDown(grip(), { key: "ArrowUp" });
			expect(sectionTitles()).toEqual(["Trends", "Overview"]);

			unmount();
			renderDashboards(sectioned);
			expect(sectionTitles()).toEqual(["Trends", "Overview"]);
			fireEvent.keyDown(grip(), { key: "ArrowDown" });
			expect(sectionTitles()).toEqual(["Overview", "Trends"]);
		});

		it("adds sections after the untitled group of a dashboard without sections, which has no options", async () => {
			renderDashboards();
			expect(
				screen.queryByRole("button", { name: /^Section options for / }),
			).toBeNull();
			fireEvent.click(screen.getByRole("button", { name: "Add section" }));
			type("Mine");
			fireEvent.keyDown(titleField(), { key: "Enter" });
			// The untitled group keeps its tiles, above; the dashboard's own +
			// still adds to it, and the new section has its own.
			expect(sectionTitles()).toEqual(["Mine"]);
			expect(tileTitles()).toEqual(DEFAULT_ORDER);
			expect(
				screen
					.getAllByRole("button", { name: /^Add a widget back to / })
					.map((button) => button.getAttribute("aria-label")),
			).toEqual(["Add a widget back to Activity", "Add a widget back to Mine"]);
			// Only the viewer's section can move or be renamed.
			expect(
				screen.getAllByRole("button", { name: /drag the section/ }),
			).toHaveLength(1);

			const grip = screen.getByRole("button", {
				name: "Move Total logins: drag the tile, or use the arrow keys",
			});
			grip.focus();
			fireEvent.keyDown(grip, { key: "ArrowDown" });
			expect(tilesIn("Mine")).toEqual(["Total logins"]);
			// Removed, its tiles go back up to the untitled group.
			await choose("Mine", "Remove section");
			expect(sectionTitles()).toEqual([]);
			expect(tileTitles()).toEqual(DEFAULT_ORDER);
		});

		describe("dragging", () => {
			// happy-dom lays nothing out, so the sections start TOP down the page,
			// under the dashboard's own header; each is a band 1000px below the
			// one before, 900px tall with a 60px header at its top, and its tiles
			// (and slot) two columns of 100×100 cells under the header.
			const TOP = 200;
			const realRect = HTMLElement.prototype.getBoundingClientRect;
			const bandOf = (element: Element | null | undefined) =>
				element?.parentElement
					? [...element.parentElement.children].indexOf(element)
					: 0;
			beforeEach(() => {
				HTMLElement.prototype.getBoundingClientRect = function (
					this: HTMLElement,
				) {
					let rect = { left: 0, top: 0, right: 100, bottom: 100 };
					const list = this.parentElement;
					if (this.dataset.sortableGroup !== undefined) {
						const top = TOP + bandOf(this) * 1000;
						rect = { left: 0, top, right: 400, bottom: top + 900 };
					} else if (this.dataset.sortableGroupHeader !== undefined) {
						const top = TOP + bandOf(this.parentElement) * 1000;
						rect = { left: 0, top, right: 400, bottom: top + 60 };
					} else if (
						this.tagName === "LI" &&
						list?.parentElement?.dataset.sortableGroup !== undefined
					) {
						const index = [...list.children].indexOf(this);
						const left = (index % 2) * 110;
						const top =
							TOP +
							bandOf(list.parentElement) * 1000 +
							100 +
							Math.floor(index / 2) * 110;
						rect = { left, top, right: left + 100, bottom: top + 100 };
					}
					return {
						...rect,
						x: rect.left,
						y: rect.top,
						width: rect.right - rect.left,
						height: rect.bottom - rect.top,
						toJSON: () => ({}),
					} as DOMRect;
				};
			});
			afterEach(async () => {
				HTMLElement.prototype.getBoundingClientRect = realRect;
				// dnd-kit keeps swallowing clicks for 50ms after a drag ends.
				await new Promise((resolve) => setTimeout(resolve, 60));
			});

			// A section picked up by its grip and aimed at a height from the top of
			// the sections (negative is above them).
			function pickUpSection(title: string) {
				const grip = screen.getByRole("button", {
					name: `Move ${title}: drag the section, or use the arrow keys`,
				});
				const band = bandOf(grip.closest("[data-sortable-group]"));
				let at = { clientX: 20, clientY: TOP + band * 1000 + 20 };
				fireEvent.mouseDown(grip, { ...at, button: 0 });
				fireEvent.mouseMove(document, { ...at, clientY: at.clientY + 10 });
				return {
					moveTo(height: number) {
						const clientY = TOP + height;
						fireEvent.mouseMove(document, {
							clientX: at.clientX,
							clientY: (at.clientY + clientY) / 2,
						});
						at = { clientX: at.clientX, clientY };
						fireEvent.mouseMove(document, at);
					},
					drop: () => fireEvent.mouseUp(document, at),
				};
			}

			it("puts a section dropped on the top half of another above it, and keeps the order", () => {
				const { unmount } = renderDashboards(sectioned);
				const drag = pickUpSection("Trends");
				// No slot while a section is dragged; nothing moves until the drop.
				expect(screen.queryByText("Move here")).toBeNull();
				drag.moveTo(200);
				expect(sectionTitles()).toEqual(["Overview", "Trends"]);
				expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
				drag.drop();
				const moved = {
					Trends: ["Logins over time", "Top event types"],
					Overview: ["Total logins"],
				};
				expect(arrangement()).toEqual(moved);
				expect(saved().map((s: { id: string }) => s.id)).toEqual([
					"trends",
					"overview",
				]);

				unmount();
				renderDashboards(sectioned);
				expect(arrangement()).toEqual(moved);
			});

			it("puts a section dropped above the first one, over the dashboard's header, first", () => {
				renderDashboards(sectioned);
				const drag = pickUpSection("Trends");
				drag.moveTo(-120);
				drag.drop();
				expect(sectionTitles()).toEqual(["Trends", "Overview"]);
			});

			it("puts a section dropped on the bottom half of another below it", () => {
				renderDashboards(sectioned);
				fireEvent.click(screen.getByRole("button", { name: "Add section" }));
				fireEvent.keyDown(titleField(), { key: "Enter" });
				expect(sectionTitles()).toEqual(["Overview", "Trends", "New section"]);
				const drag = pickUpSection("Overview");
				drag.moveTo(1000 + 700);
				drag.drop();
				expect(sectionTitles()).toEqual(["Trends", "Overview", "New section"]);
			});

			it("moves a tile dropped on a new section's slot into it, and still swaps tiles", () => {
				renderDashboards(sectioned);
				fireEvent.click(screen.getByRole("button", { name: "Add section" }));
				type("Mine");
				fireEvent.keyDown(titleField(), { key: "Enter" });
				const drag = pickUp(tile("Top event types"));
				drag.moveTo(within(section("Mine")).getByText("Move here"));
				drag.drop();
				expect(arrangement()).toEqual({
					Overview: ["Total logins"],
					Trends: ["Logins over time"],
					Mine: ["Top event types"],
				});

				const swap = pickUp(tile("Total logins"));
				swap.moveTo(tile("Top event types"));
				swap.drop();
				expect(arrangement()).toEqual({
					Overview: ["Top event types"],
					Trends: ["Logins over time"],
					Mine: ["Total logins"],
				});
			});
		});
	});
});
