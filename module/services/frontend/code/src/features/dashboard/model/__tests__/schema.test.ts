import { describe, expect, it } from "vitest";
import { dashboard, event, metric } from "../schema";

describe("dashboard data-graph declaration", () => {
	it("names an event type with an optional label", () => {
		expect(event("auth.login", "Logins")).toEqual({
			type: "auth.login",
			label: "Logins",
		});
	});

	it("carries a metric's aggregation and chart intent through", () => {
		const login = event("auth.login");
		const m = metric({
			title: "Logins over time",
			event: login,
			groupBy: "time",
			bucket: "day",
			chart: "line",
		});
		expect(m.event).toBe(login);
		expect(m.groupBy).toBe("time");
		expect(m.chart).toBe("line");
	});

	it("assembles metrics into a dashboard", () => {
		const d = dashboard({
			title: "Activity",
			metrics: [
				metric({ title: "Top events", groupBy: "event_type", chart: "bar" }),
			],
		});
		expect(d.title).toBe("Activity");
		expect(d.metrics).toHaveLength(1);
	});
});
