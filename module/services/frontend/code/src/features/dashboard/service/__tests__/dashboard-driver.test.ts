import { describe, expect, it } from "vitest";
import { validateDashboardDef } from "../../model/validate";
import { applyCommand, metricFromCommand } from "../dashboard-driver";

// The stub driver is the local stand-in for the eventual conversational agent.
// These tests exercise the whole "describe → mutate → validate" loop with no
// model call and no running service, which is the point: the canvas is testable
// here on its own.

describe("dashboard driver — natural language to spec", () => {
	it("maps a time-series request to a line metric grouped by time", () => {
		const m = metricFromCommand("show me logins over time daily");
		expect(m.chart).toBe("line");
		expect(m.groupBy).toBe("time");
		expect(m.bucket).toBe("day");
		expect(m.event).toEqual({ type: "auth.login" });
	});

	it("maps a ranking request to a bar metric with a top-N limit", () => {
		const m = metricFromCommand("top event types by event");
		expect(m.chart).toBe("bar");
		expect(m.groupBy).toBe("event_type");
		expect(m.limit).toBe(6);
		expect(m.bucket).toBeUndefined();
	});

	it("maps a total request to a stat metric", () => {
		const m = metricFromCommand("total number of TOTP verifications");
		expect(m.chart).toBe("stat");
		expect(m.event).toEqual({ type: "mfa.totp_verified" });
	});

	it("scopes a security request to the security category", () => {
		const m = metricFromCommand("security events over time by category");
		expect(m.category).toBe("security");
		expect(m.groupBy).toBe("category");
	});

	it("every produced metric is a valid dashboard spec", () => {
		const commands = [
			"logins over time",
			"top actions by event",
			"total logins",
			"security events by category",
			"mfa over time weekly",
		];
		for (const command of commands) {
			const result = applyCommand(undefined, command);
			expect(validateDashboardDef(result.dashboard).ok).toBe(true);
		}
	});
});

describe("dashboard driver — structural commands", () => {
	it("appends a widget and reports it", () => {
		const first = applyCommand(undefined, "logins over time");
		expect(first.dashboard.metrics).toHaveLength(1);
		const second = applyCommand(first.dashboard, "top event types");
		expect(second.dashboard.metrics).toHaveLength(2);
		expect(second.note).toContain("Added");
	});

	it("clears all widgets", () => {
		const built = applyCommand(
			applyCommand(undefined, "logins over time").dashboard,
			"top event types",
		);
		const cleared = applyCommand(built.dashboard, "clear");
		expect(cleared.dashboard.metrics).toHaveLength(0);
	});

	it("removes the last widget", () => {
		const built = applyCommand(
			applyCommand(undefined, "logins over time").dashboard,
			"top event types",
		);
		const removed = applyCommand(built.dashboard, "remove the last widget");
		expect(removed.dashboard.metrics).toHaveLength(1);
		expect(removed.dashboard.metrics[0].title).toContain("Logins");
	});

	it("treats an empty command as a no-op", () => {
		const built = applyCommand(undefined, "logins over time");
		const noop = applyCommand(built.dashboard, "   ");
		expect(noop.dashboard).toBe(built.dashboard);
	});
});
