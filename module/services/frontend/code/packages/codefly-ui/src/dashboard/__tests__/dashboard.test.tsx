// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Dashboard } from "../dashboard.js";
import type { DashboardView } from "../types.js";

afterEach(cleanup);

const emptySeries = { points: [], total: 0 };

describe("Dashboard", () => {
	it("renders its header through Section and each widget through Card", () => {
		const data: DashboardView = {
			title: "Traffic",
			description: "last 7 days",
			widgets: [
				{
					id: "w1",
					title: "Visits",
					visualization: "number",
					series: emptySeries,
				},
			],
		};
		const { container } = render(<Dashboard data={data} />);

		// Section owns the header markup — its <section> root and heading classes.
		const section = container.querySelector("section");
		expect(section).not.toBeNull();
		expect(
			screen.getByRole("heading", { name: "Traffic", level: 2 }),
		).toBeTruthy();
		expect(screen.getByText("last 7 days")).toBeTruthy();

		// Card owns the surface — one card per widget, painted by the primitive.
		const card = container.querySelector(
			".rounded-lg.border.bg-card.p-4.text-card-foreground.shadow-sm",
		);
		expect(card).not.toBeNull();
		expect(
			screen.getByRole("heading", { name: "Visits", level: 3 }),
		).toBeTruthy();
	});

	it("scopes the accent override onto the Section root", () => {
		const data: DashboardView = {
			accent: "hotpink",
			widgets: [{ id: "w1", visualization: "number", series: emptySeries }],
		};
		const { container } = render(<Dashboard data={data} />);
		const section = container.querySelector("section") as HTMLElement;
		expect(section.style.getPropertyValue("--primary")).toBe("hotpink");
		// A line or area chart's one series colours from the palette's last token.
		expect(section.style.getPropertyValue("--chart-5")).toBe("hotpink");
	});

	it("draws a line widget's series in the palette's last colour, which the accent sets", () => {
		const { container } = render(
			<Dashboard
				data={{
					accent: "hotpink",
					widgets: [
						{
							id: "w1",
							title: "Visits",
							visualization: "line",
							series: {
								points: [
									{ key: "2026-09-01", value: 1 },
									{ key: "2026-09-02", value: 3 },
								],
								total: 4,
							},
						},
					],
				}}
			/>,
		);
		expect(
			container.querySelector('path[stroke="var(--chart-5)"]'),
		).toBeTruthy();
	});
});

it("preserves partial telemetry and unavailable totals from the SDK view", () => {
	render(
		<Dashboard
			data={{
				widgets: [
					{
						id: "partial",
						visualization: "number",
						series: {
							points: [{ key: "observed", value: 4 }],
							total: null,
							coverage: "partial",
						},
					},
				],
			}}
		/>,
	);
	expect(screen.getByRole("status").textContent).toBe("Partial telemetry");
	expect(screen.getByText("Total unavailable")).toBeTruthy();
	expect(screen.queryByText("0")).toBeNull();
});

it("writes a percent widget's values as percentages and a plain one as a number", () => {
	render(
		<Dashboard
			data={{
				widgets: [
					{
						id: "rate",
						visualization: "number",
						format: "percent",
						series: { points: [{ key: "all", value: 0.4 }], total: 0.4 },
					},
					{
						id: "outcomes",
						visualization: "bar",
						format: "percent",
						series: {
							points: [
								{ key: "won", value: 0.25 },
								{ key: "lost", value: 0.75 },
							],
							total: 1,
						},
					},
					{
						id: "versions",
						visualization: "number",
						series: { points: [{ key: "all", value: 2.25 }], total: 2.25 },
					},
				],
			}}
		/>,
	);
	expect(screen.getByText("40%")).toBeTruthy();
	expect(screen.getByText("25%")).toBeTruthy();
	expect(screen.getByText("75%")).toBeTruthy();
	expect(screen.getByText((2.25).toLocaleString())).toBeTruthy();
	expect(screen.queryByText("0.4")).toBeNull();
});
