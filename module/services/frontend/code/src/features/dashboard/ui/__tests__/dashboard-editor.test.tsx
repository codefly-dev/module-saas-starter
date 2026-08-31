import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAuth } from "@/lib/auth";
import { useAuditService } from "@/lib/hooks/use-api-client";
import { renderInApp } from "@/test/container";
import { dashboard, metric } from "../../model/schema";
import { DashboardEditor } from "../dashboard-editor";

vi.mock("@/lib/hooks/use-api-client", () => ({ useAuditService: vi.fn() }));
vi.mock("@/lib/auth", () => ({ useAuth: vi.fn() }));
// Stub the canvas: the editor owns its own widget list and commit loop, which is
// what these tests exercise; the pure renderer has its own coverage.
vi.mock("../dashboard", () => ({
	Dashboard: () => <div data-testid="canvas" />,
}));

const eventTypes = [
	{
		$typeName: "saas.accounts.v1.AuditEventType",
		name: "auth.login",
		version: 1,
		category: "authentication",
		owner: "accounts",
		deprecated: false,
		description: "A user logged in.",
	},
];

// Two aggregate buckets so a count preview totals 8 across 2 points.
const twoBuckets = [
	{ key: "2026-08-03", count: "3", keys: ["2026-08-03"], metrics: {} },
	{ key: "2026-08-10", count: "5", keys: ["2026-08-10"], metrics: {} },
];

function fakeAuditService(buckets: unknown[] = []) {
	return {
		listAuditEventTypes: vi.fn(async () => ({
			$typeName: "saas.accounts.v1.ListAuditEventTypesResponse",
			types: eventTypes,
		})),
		aggregateAuditLog: vi.fn(async () => ({
			$typeName: "saas.accounts.v1.AggregateAuditLogResponse",
			buckets,
		})),
	};
}

function mount(buckets: unknown[] = [], orgId: string | null = "org-1") {
	vi.mocked(useAuditService).mockReturnValue(
		fakeAuditService(buckets) as unknown as ReturnType<typeof useAuditService>,
	);
	vi.mocked(useAuth).mockReturnValue({
		organizationId: orgId,
	} as unknown as ReturnType<typeof useAuth>);
}

beforeEach(() => {
	window.localStorage.clear();
});

afterEach(() => {
	vi.restoreAllMocks();
	window.localStorage.clear();
});

describe("DashboardEditor", () => {
	it("adds a widget from the default form and re-renders the draft", async () => {
		mount();
		renderInApp(
			<DashboardEditor
				storageKey="dashboard:test"
				initial={dashboard({ title: "T", metrics: [] })}
			/>,
		);

		expect(screen.getByText(/No widgets yet/)).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Add widget" }));

		// The default form is an all-events time-series line, so the added widget
		// carries the derived title.
		expect(await screen.findByText("Events over time")).toBeTruthy();
		expect(screen.queryByText(/No widgets yet/)).toBeNull();
	});

	it("rejects a second widget with the same identity instead of duplicating it", async () => {
		mount();
		renderInApp(
			<DashboardEditor
				storageKey="dashboard:test"
				initial={dashboard({ title: "T", metrics: [] })}
			/>,
		);

		const add = screen.getByRole("button", { name: "Add widget" });
		fireEvent.click(add);
		expect(await screen.findByText("Events over time")).toBeTruthy();

		// The default form is unchanged after the first add, so a second click would
		// emit an identical metric that collides on <Dashboard>'s React key.
		fireEvent.click(add);
		expect(await screen.findByText(/already shows/)).toBeTruthy();
		// The widget-list title (exact match) appears exactly once; the rejection
		// message embeds it in a longer sentence, so it isn't a second match.
		expect(screen.getAllByText("Events over time")).toHaveLength(1);
	});

	it("reorders and removes existing widgets through the authoring commit path", async () => {
		mount();
		renderInApp(
			<DashboardEditor
				storageKey="dashboard:test"
				initial={dashboard({
					title: "T",
					metrics: [
						metric({ title: "Alpha", groupBy: "event_type", chart: "bar" }),
						metric({ title: "Beta", groupBy: "category", chart: "bar" }),
					],
				})}
			/>,
		);

		const order = () =>
			screen.getAllByText(/^(Alpha|Beta)$/).map((el) => el.textContent);
		expect(order()).toEqual(["Alpha", "Beta"]);

		fireEvent.click(screen.getByRole("button", { name: 'Move "Beta" up' }));
		await waitFor(() => expect(order()).toEqual(["Beta", "Alpha"]));

		fireEvent.click(screen.getByRole("button", { name: 'Remove "Alpha"' }));
		await waitFor(() => expect(screen.queryByText("Alpha")).toBeNull());
		expect(screen.getByText("Beta")).toBeTruthy();
	});

	it("previews the drafted metric against live audit data", async () => {
		mount(twoBuckets);
		renderInApp(
			<DashboardEditor
				storageKey="dashboard:test"
				initial={dashboard({ title: "T", metrics: [] })}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Preview" }));
		expect(await screen.findByText("Preview: 8 across 2 points.")).toBeTruthy();
	});

	it("surfaces a pending precondition when no organization is in scope", async () => {
		mount([], null);
		renderInApp(
			<DashboardEditor
				storageKey="dashboard:test"
				initial={dashboard({ title: "T", metrics: [] })}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Preview" }));
		expect(
			await screen.findByText(/No organization is in scope yet/),
		).toBeTruthy();
	});
});
