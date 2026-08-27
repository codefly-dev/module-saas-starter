import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Dashboard, type DashboardData } from "./dashboard";

afterEach(cleanup);

function renderDashboard(data: DashboardData) {
	return render(<Dashboard data={data} />);
}

describe("Dashboard renderer", () => {
	it("renders the header title, description, and actions", () => {
		renderDashboard({
			title: "Audit Log",
			description: "System events.",
			actions: <button type="button">Export</button>,
			widgets: [],
		});
		expect(screen.getByText("Audit Log")).toBeTruthy();
		expect(screen.getByText("System events.")).toBeTruthy();
		expect(screen.getByRole("button", { name: "Export" })).toBeTruthy();
	});

	it("renders a bars widget with labels and values", () => {
		renderDashboard({
			widgets: [
				{
					id: "top",
					kind: "bars",
					title: "Top event types",
					items: [
						{ label: "user.login", value: 12 },
						{ label: "user.logout", value: 3 },
					],
				},
			],
		});
		expect(screen.getByText("Top event types")).toBeTruthy();
		expect(screen.getByText("user.login")).toBeTruthy();
		expect(screen.getByText("12")).toBeTruthy();
	});

	it("renders a node widget bare (no card chrome)", () => {
		renderDashboard({
			widgets: [
				{ id: "table", kind: "node", node: <table aria-label="events" /> },
			],
		});
		expect(screen.getByLabelText("events")).toBeTruthy();
	});

	it("shows a skeleton while a widget is loading", () => {
		const { container } = renderDashboard({
			widgets: [
				{ id: "s", kind: "sparkline", title: "Trend", points: [], isLoading: true },
			],
		});
		expect(container.querySelector('[data-slot="skeleton"]')).toBeTruthy();
	});

	it("shows an error message when a widget errors", () => {
		renderDashboard({
			widgets: [
				{
					id: "s",
					kind: "sparkline",
					title: "Trend",
					points: [],
					error: new Error("boom"),
				},
			],
		});
		expect(screen.getByText("Failed to load.")).toBeTruthy();
	});

	it("shows the empty message when a widget resolves to no data", () => {
		renderDashboard({
			widgets: [
				{
					id: "s",
					kind: "sparkline",
					title: "Trend",
					points: [],
					emptyMessage: "No events in range.",
				},
			],
		});
		expect(screen.getByText("No events in range.")).toBeTruthy();
	});

	it("surfaces error before loading and empty when there is no data", () => {
		renderDashboard({
			widgets: [
				{
					id: "s",
					kind: "bars",
					items: [],
					isLoading: true,
					error: new Error("boom"),
					emptyMessage: "Nothing here.",
				},
			],
		});
		expect(screen.getByText("Failed to load.")).toBeTruthy();
		expect(screen.queryByText("Nothing here.")).toBeNull();
	});

	it("keeps rendering retained data when a background refetch errors", () => {
		// A widget that still holds data must not blank to an error state on a
		// transient refetch failure — the last good content stays on screen.
		renderDashboard({
			widgets: [
				{
					id: "s",
					kind: "bars",
					title: "Top event types",
					items: [{ label: "user.login", value: 7 }],
					error: new Error("refetch blip"),
					emptyMessage: "No events in range.",
				},
			],
		});
		expect(screen.getByText("user.login")).toBeTruthy();
		expect(screen.getByText("7")).toBeTruthy();
		expect(screen.queryByText("Failed to load.")).toBeNull();
	});
});
