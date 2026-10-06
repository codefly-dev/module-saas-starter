/**
 * The middleware's own answer on a well-known path.
 *
 * `isPublic` is unit-tested beside the predicate, but the condition that
 * matters to a client is the RESPONSE: a discovery request must not be answered
 * with a redirect to a login page. A non-browser client follows that redirect,
 * parses an HTML page as JSON, and reports that this host is not an
 * authorization server — so this drives the middleware and asserts there is no
 * 307, wherever in the tree the document sits.
 */

import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { proxy } from "@/proxy";

describe("well-known paths are never redirected to login", () => {
	beforeEach(() => {
		vi.stubEnv(
			"SOLUTION_CSP_INPUTS",
			JSON.stringify({
				solutionOrigins: [],
				analyticsOrigin: null,
				turnstile: false,
			}),
		);
	});
	afterEach(() => {
		vi.unstubAllEnvs();
	});

	it.each([
		// The RFC 8414 document a client reads to find the authorization server.
		"/.well-known/oauth-authorization-server",
		// RFC 9728 §3.1's constructed location, at the host root.
		"/.well-known/oauth-protected-resource/api/solutions/example/proxy/mcp",
		// Beneath a solution's own route, which is a page of this app — the case
		// a prefix test on "/.well-known/" leaves behind the login redirect.
		"/solutions/example/.well-known/oauth-protected-resource",
		// One this host does not serve at all. It must reach the handler that
		// answers 404: "no document here" is a true answer a client can act on.
		"/.well-known/openid-configuration",
	])("does not redirect %s", async (pathname) => {
		const response = await proxy(
			new NextRequest(`https://app.example${pathname}`),
		);

		expect(response.status).not.toBe(307);
		expect(response.headers.get("location")).toBeNull();
	});

	it("still redirects a protected page, so the assertion above is not vacuous", async () => {
		const response = await proxy(new NextRequest("https://app.example/settings"));
		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toContain("/auth/login");
	});
});
