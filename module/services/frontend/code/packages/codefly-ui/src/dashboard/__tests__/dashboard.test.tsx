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
	});
});
