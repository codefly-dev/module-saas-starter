import { describe, expect, it } from "vitest";
import {
	baselineSecurityHeaders,
	contentSecurityPolicy,
	parseSolutionOrigins,
	securityHeaders,
} from "../../server/security-headers.mjs";

function directive(csp: string, name: string): string {
	const found = csp
		.split(";")
		.map((part) => part.trim())
		.find((part) => part === name || part.startsWith(`${name} `));
	if (!found) {
		throw new Error(`directive ${name} missing from: ${csp}`);
	}
	return found;
}

describe("securityHeaders", () => {
	it("always denies framing and sets the hardening baseline", () => {
		const headers = securityHeaders({});
		const byKey = Object.fromEntries(headers.map((h) => [h.key, h.value]));
		expect(byKey["X-Frame-Options"]).toBe("DENY");
		expect(byKey["X-Content-Type-Options"]).toBe("nosniff");
		expect(byKey["Cross-Origin-Opener-Policy"]).toBe("same-origin");
		expect(byKey["Referrer-Policy"]).toBe("strict-origin-when-cross-origin");
		expect(byKey["Content-Security-Policy"]).toContain(
			"frame-ancestors 'none'",
		);
	});
});

describe("baselineSecurityHeaders", () => {
	it("carries the hardening headers but no CSP (the proxy owns it)", () => {
		const byKey = Object.fromEntries(
			baselineSecurityHeaders().map((h) => [h.key, h.value]),
		);
		expect(byKey["X-Frame-Options"]).toBe("DENY");
		expect(byKey["X-Content-Type-Options"]).toBe("nosniff");
		expect(byKey["Cross-Origin-Opener-Policy"]).toBe("same-origin");
		expect(byKey["Content-Security-Policy"]).toBeUndefined();
	});
});

describe("contentSecurityPolicy", () => {
	it("locks down to self by default", () => {
		const csp = contentSecurityPolicy({});
		expect(directive(csp, "default-src")).toBe("default-src 'self'");
		expect(directive(csp, "object-src")).toBe("object-src 'none'");
		expect(directive(csp, "frame-ancestors")).toBe("frame-ancestors 'none'");
		expect(directive(csp, "script-src")).toBe(
			"script-src 'self' 'unsafe-inline'",
		);
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		// The /docs API viewer iframe is always embedded, so frame-src always
		// allows it; Turnstile is not added unless bot protection is on.
		expect(directive(csp, "frame-src")).toBe(
			"frame-src 'self' https://petstore.swagger.io",
		);
		// Avatars are user-supplied external image URLs.
		expect(directive(csp, "img-src")).toBe("img-src 'self' data: https:");
	});

	it("allowlists registered Module Federation solution origins", () => {
		const csp = contentSecurityPolicy({
			FRONTEND_SOLUTION_ORIGINS:
				"https://a.example.com, https://b.example.com/ignored-path",
		});
		expect(directive(csp, "script-src")).toContain("https://a.example.com");
		expect(directive(csp, "script-src")).toContain("https://b.example.com");
		expect(directive(csp, "connect-src")).toContain("https://a.example.com");
		expect(directive(csp, "connect-src")).toContain("https://b.example.com");
	});

	it("allowlists runtime-derived solution origins without a build-time env", () => {
		const csp = contentSecurityPolicy({}, ["http://localhost:8091"]);
		expect(directive(csp, "script-src")).toBe(
			"script-src 'self' 'unsafe-inline' http://localhost:8091",
		);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("merges and dedupes runtime origins with the build-time allowlist", () => {
		const csp = contentSecurityPolicy(
			{ FRONTEND_SOLUTION_ORIGINS: "https://a.example.com" },
			["https://a.example.com", "http://localhost:8091"],
		);
		const scriptSrc = directive(csp, "script-src");
		expect(scriptSrc).toBe(
			"script-src 'self' 'unsafe-inline' https://a.example.com http://localhost:8091",
		);
		expect(scriptSrc.match(/https:\/\/a\.example\.com/g)).toHaveLength(1);
	});

	it("allowlists the PostHog ingestion host only when analytics is enabled", () => {
		const enabled = contentSecurityPolicy({
			NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE: "posthog",
			NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
		});
		expect(directive(enabled, "connect-src")).toContain(
			"https://eu.i.posthog.com",
		);
		const disabled = contentSecurityPolicy({
			NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
		});
		expect(directive(disabled, "connect-src")).not.toContain("posthog");
	});

	it("allowlists Cloudflare Turnstile only when abuse protection is enabled", () => {
		const csp = contentSecurityPolicy({
			NEXT_PUBLIC_ABUSE_PROTECTION_MODE: "turnstile",
		});
		expect(directive(csp, "script-src")).toContain(
			"https://challenges.cloudflare.com",
		);
		expect(directive(csp, "connect-src")).toContain(
			"https://challenges.cloudflare.com",
		);
		expect(directive(csp, "frame-src")).toBe(
			"frame-src 'self' https://petstore.swagger.io https://challenges.cloudflare.com",
		);
	});
});

describe("parseSolutionOrigins", () => {
	it("normalizes to origins and dedupes", () => {
		expect(
			parseSolutionOrigins("https://a.example.com/x https://a.example.com/y"),
		).toEqual(["https://a.example.com"]);
	});

	it("returns empty for unset input", () => {
		expect(parseSolutionOrigins(undefined)).toEqual([]);
		expect(parseSolutionOrigins("")).toEqual([]);
	});

	it("rejects non-absolute or credentialed origins", () => {
		expect(() => parseSolutionOrigins("/relative")).toThrow();
		expect(() =>
			parseSolutionOrigins("https://user:pass@example.com"),
		).toThrow();
		expect(() => parseSolutionOrigins("ftp://example.com")).toThrow();
	});
});
