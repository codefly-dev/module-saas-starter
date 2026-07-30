import { describe, expect, it } from "vitest";
import { configuredErrorTracking } from "./error-tracking";

describe("error tracking configuration", () => {
	it("is explicitly disabled without a DSN", () => {
		expect(configuredErrorTracking("disabled", "")).toEqual({
			enabled: false,
		});
	});

	it("fails closed on partial, conflicting, and unknown configuration", () => {
		expect(() =>
			configuredErrorTracking("disabled", "https://key@sentry.example/1"),
		).toThrow("while ERROR_TRACKING_MODE is disabled");
		expect(() => configuredErrorTracking("sentry", "")).toThrow(
			"required when ERROR_TRACKING_MODE=sentry",
		);
		expect(() => configuredErrorTracking("typo", "")).toThrow(
			"disabled or sentry",
		);
	});

	it("accepts a secure Sentry DSN", () => {
		expect(
			configuredErrorTracking(
				"sentry",
				"https://public-key@o1.ingest.sentry.io/42",
			),
		).toEqual({
			enabled: true,
			dsn: "https://public-key@o1.ingest.sentry.io/42",
		});
	});
});
