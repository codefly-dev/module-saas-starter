import { describe, expect, it } from "vitest";
import { configuredAbuseProtection } from "./abuse-protection";

describe("browser abuse protection configuration", () => {
	it("is disabled by default", () => {
		expect(configuredAbuseProtection(undefined, undefined)).toEqual({
			enabled: false,
		});
	});

	it("fails closed on partial, conflicting, and unknown configuration", () => {
		expect(() => configuredAbuseProtection("turnstile", "")).toThrow(
			"site key is required",
		);
		expect(() => configuredAbuseProtection("disabled", "site-key")).toThrow(
			"while abuse protection is disabled",
		);
		expect(() => configuredAbuseProtection("captcha", "")).toThrow(
			"disabled or turnstile",
		);
	});
});
