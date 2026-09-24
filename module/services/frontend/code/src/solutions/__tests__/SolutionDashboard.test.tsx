import type { DataGraph } from "@codefly/saas-plugin-manifest";
import { cleanup, fireEvent, screen, within } from "@testing-library/react";
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
		// Declared but drawn by no widget: only a viewer can put it on the page.
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

	it("adds a removed widget and an undrawn metric back from the + menu", async () => {
		renderDashboards();
		fireEvent.click(
			screen.getByRole("button", { name: "Remove Top event types" }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: "Add a metric to Activity" }),
		);
		const items = await screen.findAllByRole("menuitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"Top event types",
			"Logins by category",
		]);

		fireEvent.click(screen.getByRole("menuitem", { name: "Top event types" }));
		fireEvent.click(
			screen.getByRole("button", { name: "Add a metric to Activity" }),
		);
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "Logins by category" }),
		);

		expect(tileTitles()).toEqual([
			"Logins over time",
			"Total logins",
			"Top event types",
			"Logins by category",
		]);
		// The added metric resolves through the same path as a declared widget:
		// a category count, shown as a single number (3 + 5).
		expect(
			await within(tile("Logins by category")).findByText("8"),
		).toBeTruthy();
	});

	it("reflows while a tile is dragged and saves the order on drop", () => {
		const { unmount } = renderDashboards();
		const dragged = tile("Logins over time");
		fireEvent.dragStart(dragged, { dataTransfer: {} });
		fireEvent.dragOver(tile("Total logins"), { dataTransfer: {} });
		// The other tiles shift while the drag is still in flight...
		expect(tileTitles()).toEqual([
			"Top event types",
			"Total logins",
			"Logins over time",
		]);
		// ...and nothing is saved until the drop.
		expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();

		fireEvent.drop(tile("Logins over time"), { dataTransfer: {} });
		fireEvent.dragEnd(dragged, { dataTransfer: {} });
		expect(tileTitles()).toEqual([
			"Top event types",
			"Total logins",
			"Logins over time",
		]);

		unmount();
		renderDashboards();
		expect(tileTitles()).toEqual([
			"Top event types",
			"Total logins",
			"Logins over time",
		]);
	});

	it("keeps the new order when a tile is dropped in the gap between tiles", () => {
		renderDashboards();
		const dragged = tile("Total logins");
		fireEvent.dragStart(dragged, { dataTransfer: {} });
		fireEvent.dragOver(tile("Logins over time"), { dataTransfer: {} });
		fireEvent.drop(screen.getByRole("list"), { dataTransfer: {} });
		fireEvent.dragEnd(dragged, { dataTransfer: {} });
		expect(tileTitles()).toEqual([
			"Total logins",
			"Logins over time",
			"Top event types",
		]);
		expect(window.localStorage.getItem(LAYOUT_KEY)).not.toBeNull();
	});

	it("reverts a cancelled drag", () => {
		renderDashboards();
		const dragged = tile("Total logins");
		fireEvent.dragStart(dragged, { dataTransfer: {} });
		fireEvent.dragOver(tile("Logins over time"), { dataTransfer: {} });
		expect(tileTitles()[0]).toBe("Total logins");

		fireEvent.dragEnd(dragged, { dataTransfer: {} });
		expect(tileTitles()).toEqual(DEFAULT_ORDER);
		expect(window.localStorage.getItem(LAYOUT_KEY)).toBeNull();
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
