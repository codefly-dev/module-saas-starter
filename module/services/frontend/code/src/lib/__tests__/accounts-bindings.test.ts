import { describe, expect, it } from "vitest";

import {
	requireAccountsConnect,
	resolveAccountsBindings,
	resolveProductAPIRewrites,
} from "../../../server/accounts-bindings.mjs";

describe("server-only accounts bindings", () => {
	const accountsEndpoint = (
		name: "rest" | "connect",
		protocol: "REST" | "CONNECT",
		address: string,
	) => ({
		module: "saas",
		service: "accounts",
		name,
		protocol,
		address,
		routes: [],
	});
	const gatewayEndpoint = (address: string) => ({
		module: "saas",
		service: "auth-gateway",
		name: "rest",
		protocol: "REST" as const,
		address,
		routes: [],
	});
	const directAccounts = [
		accountsEndpoint("rest", "REST", "http://accounts-rest.internal/"),
		accountsEndpoint("connect", "CONNECT", "http://accounts-connect.internal/"),
	];

	it("routes both REST and Connect through the discovered gateway", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [
					...directAccounts,
					gatewayEndpoint("http://auth-gateway.internal/"),
				],
			}),
		).toEqual({
			rest: "http://auth-gateway.internal",
			connect: "http://auth-gateway.internal",
		});
	});

	it("accepts an explicit gateway override outside the module graph", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				endpoints: [],
				environment: { PRODUCT_GATEWAY_INTERNAL: "http://localhost:21999/" },
			}),
		).toEqual({
			rest: "http://localhost:21999",
			connect: "http://localhost:21999",
		});
	});

	it("fails closed when no gateway is configured", () => {
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				endpoints: [],
				environment: {},
			}),
		).toThrow(/did not resolve auth-gateway\/rest/);
	});

	it("refuses an ambiguous gateway rather than picking one", () => {
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [
					gatewayEndpoint("http://auth-gateway-a.internal"),
					gatewayEndpoint("http://auth-gateway-b.internal"),
				],
			}),
		).toThrow(/multiple auth-gateway\/rest endpoints/);
	});

	it.each([
		"ftp://auth-gateway.internal",
		"https://user:secret@auth-gateway.internal",
		"https://auth-gateway.internal?target=other",
		"relative",
	])("rejects a malformed gateway address: %s", (value) => {
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [gatewayEndpoint(value)],
			}),
		).toThrow();
	});

	it("never selects Accounts when only its own endpoints are discovered", () => {
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: directAccounts,
			}),
		).toThrow(/did not resolve auth-gateway\/rest/);
	});

	it.each([
		{ API_REST_INTERNAL: "http://accounts-rest.internal" },
		{ API_CONNECT_INTERNAL: "http://accounts-connect.internal" },
		{
			API_REST_INTERNAL: "http://accounts-rest.internal",
			API_CONNECT_INTERNAL: "http://accounts-connect.internal",
		},
	])(
		"refuses to start when a retired direct binding is still set: %s",
		(environment) => {
			// Even alongside a healthy gateway: a leftover direct destination is a
			// deployment that still believes it has a second API path.
			expect(() =>
				resolveAccountsBindings({
					currentModule: "saas",
					environment,
					endpoints: [gatewayEndpoint("http://auth-gateway.internal")],
				}),
			).toThrow(/no longer selects an Accounts destination/);
		},
	);

	it("keeps direct Accounts unreachable from the environment", () => {
		// The isolated seam is a call argument, so no environment shape — fixture,
		// build mode, or leftover variable — can reopen the direct path.
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				endpoints: directAccounts,
				environment: {
					NODE_ENV: "test",
					CODEFLY__FIXTURE: "dev-admin",
					PRODUCT_GATEWAY_DIRECT: "1",
					ACCOUNTS_REST_INTERNAL: "http://accounts-rest.internal",
				},
			}),
		).toThrow(/did not resolve auth-gateway\/rest/);
	});

	it("lets an explicitly injected isolated test address Accounts directly", () => {
		expect(
			resolveAccountsBindings({
				endpoints: [],
				environment: {},
				isolatedDirectAccounts: {
					rest: "http://localhost:2072/",
					connect: "http://localhost:12930/",
				},
			}),
		).toEqual({
			rest: "http://localhost:2072",
			connect: "http://localhost:12930",
		});
	});

	it("validates an isolated injection as strictly as a gateway", () => {
		expect(() =>
			resolveAccountsBindings({
				endpoints: [],
				environment: {},
				isolatedDirectAccounts: {
					rest: "http://localhost:2072",
					connect: "ftp://localhost:12930",
				},
			}),
		).toThrow(/Isolated Accounts Connect destination/);
	});

	it("gives server routes the gateway as their Connect base", () => {
		expect(
			requireAccountsConnect({
				currentModule: "saas",
				environment: {},
				endpoints: [gatewayEndpoint("http://auth-gateway.internal")],
			}),
		).toBe("http://auth-gateway.internal");
	});

	it("fails a server route closed when no gateway is configured", () => {
		expect(() =>
			requireAccountsConnect({
				currentModule: "saas",
				endpoints: [],
				environment: {},
			}),
		).toThrow(/did not resolve auth-gateway\/rest/);
	});

	it("gives the browser and the server the same gateway binding", () => {
		// The browser reaches Accounts through the Next rewrites; a server route
		// dials the gateway itself. Both must name one destination, or the two
		// halves of a journey would cross different security paths.
		const options = {
			currentModule: "saas",
			environment: {},
			endpoints: [
				...directAccounts,
				gatewayEndpoint("http://auth-gateway.internal"),
			],
		};
		const browserPath = resolveProductAPIRewrites(options);
		expect(browserPath).toEqual({
			rest: requireAccountsConnect(options),
			connect: requireAccountsConnect(options),
		});
	});

	describe("build-time rewrite destinations", () => {
		it("names the gateway when the build can resolve one", () => {
			expect(
				resolveProductAPIRewrites({
					currentModule: "saas",
					environment: {},
					endpoints: [gatewayEndpoint("http://auth-gateway.internal")],
				}),
			).toEqual({
				rest: "http://auth-gateway.internal",
				connect: "http://auth-gateway.internal",
			});
		});

		it("emits no destination rather than naming Accounts", () => {
			// An image is built outside the module graph, so this must not fail the
			// build — but it must also never produce a second API path.
			expect(
				resolveProductAPIRewrites({
					currentModule: "saas",
					environment: {},
					endpoints: directAccounts,
				}),
			).toBeUndefined();
		});

		it("fails the build on a retired direct binding", () => {
			expect(() =>
				resolveProductAPIRewrites({
					currentModule: "saas",
					endpoints: [],
					environment: { API_REST_INTERNAL: "http://accounts-rest.internal" },
				}),
			).toThrow(/no longer selects an Accounts destination/);
		});
	});
});
