import { describe, expect, it } from "vitest";

import {
	requireAccountsConnect,
	resolveAccountsBindings,
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
		service: "auth-sidecar",
		name: "rest",
		protocol: "REST" as const,
		address,
		routes: [],
	});

	it("uses auth-sidecar as the REST and Connect gateway in a module run", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [
					accountsEndpoint(
						"rest",
						"REST",
						"http://accounts-rest.internal/",
					),
					accountsEndpoint(
						"connect",
						"CONNECT",
						"http://accounts-connect.internal/",
					),
					gatewayEndpoint("http://auth-sidecar.internal/"),
				],
			}),
		).toEqual({
			rest: "http://auth-sidecar.internal",
			connect: "http://auth-sidecar.internal",
		});
	});

	it("normalizes Codefly REST and Connect endpoints", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [
					accountsEndpoint("rest", "REST", "http://accounts-rest.internal/"),
					accountsEndpoint(
						"connect",
						"CONNECT",
						"https://accounts-connect.internal/",
					),
				],
			}),
		).toEqual({
			rest: "http://accounts-rest.internal",
			connect: "https://accounts-connect.internal",
		});
	});

	it.each([
		"ftp://accounts.internal",
		"https://user:secret@accounts.internal",
		"https://accounts.internal?target=other",
		"relative",
	])("rejects an unsafe server destination: %s", (value) => {
		expect(() =>
			resolveAccountsBindings({
				currentModule: "saas",
				environment: {},
				endpoints: [accountsEndpoint("connect", "CONNECT", value)],
			}),
		).toThrow();
	});

	it("uses explicit server-only endpoints outside the Codefly runtime", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				endpoints: [],
				environment: {
					API_REST_INTERNAL: "http://localhost:2072",
					API_CONNECT_INTERNAL: "http://localhost:12930",
				},
			}),
		).toEqual({
			rest: "http://localhost:2072",
			connect: "http://localhost:12930",
		});
	});

	it("fails closed when a server route has no Connect binding", () => {
		expect(() =>
			requireAccountsConnect({
				currentModule: "saas",
				endpoints: [],
				environment: {},
			}),
		).toThrow(/Codefly product API gateway/);
	});
});
