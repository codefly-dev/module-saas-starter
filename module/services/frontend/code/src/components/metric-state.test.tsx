import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
	assertSampleModeAllowed,
	MetricProvenance,
	type MetricState,
	MetricStateBadge,
} from "./metric-state";

describe("metric state taxonomy", () => {
	it.each([
		["loading", "Loading"],
		["no_data", "No data"],
		["partial", "Partial data"],
		["stale", "Stale"],
		["provider_unavailable", "Provider unavailable"],
		["not_configured", "Not configured"],
		["sample", "Sample data"],
	] satisfies [MetricState, string][])("renders %s", (state, label) => {
		render(<MetricStateBadge state={state} />);
		expect(screen.getByText(label)).toBeTruthy();
	});

	it("blocks sample mode in production", () => {
		expect(() => assertSampleModeAllowed(true, "production")).toThrow(
			"disabled in production",
		);
		expect(() => assertSampleModeAllowed(false, "production")).not.toThrow();
	});

	it("shows source, freshness, timezone, and owner", () => {
		render(
			<MetricProvenance
				source="UsageService"
				observedAt="2026-07-28T10:00:00Z"
				owner="product"
			/>,
		);
		expect(screen.getByText("Source: UsageService")).toBeTruthy();
		expect(screen.getByText("Timezone: UTC")).toBeTruthy();
		expect(screen.getByText("Owner: product")).toBeTruthy();
		expect(screen.getByText(/Freshness:/)).toBeTruthy();
	});
});
