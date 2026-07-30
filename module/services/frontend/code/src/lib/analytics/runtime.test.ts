import { describe, expect, it } from "vitest";
import { createBrowserAnalytics } from "./runtime";

describe("browser analytics runtime configuration", () => {
	it("defaults to a consent-gated disabled runtime", async () => {
		const runtime = createBrowserAnalytics({
			mode: "disabled",
			storage: window.localStorage,
		});
		expect(
			await runtime.track("signup_started", {
				flow_version: "v1",
				provider: "workos",
			}),
		).toBe(false);
	});

	it("fails closed for partial or conflicting PostHog configuration", () => {
		expect(() =>
			createBrowserAnalytics({
				mode: "posthog",
				storage: window.localStorage,
				host: "https://eu.i.posthog.com",
			}),
		).toThrow("host and project key");
		expect(() =>
			createBrowserAnalytics({
				mode: "disabled",
				storage: window.localStorage,
				apiKey: "phc_accidental",
			}),
		).toThrow("while product analytics is disabled");
	});
});
