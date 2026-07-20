import { describe, expect, it } from "vitest";

import {
	requireAccountsConnect,
	resolveAccountsBindings,
} from "../../../server/accounts-bindings.mjs";

describe("server-only accounts bindings", () => {
	const endpoint = (
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

	it("normalizes Codefly REST and Connect endpoints", () => {
		expect(
			resolveAccountsBindings({
				currentModule: "saas",
				endpoints: [
					endpoint("rest", "REST", "http://accounts-rest.internal/"),
					endpoint("connect", "CONNECT", "https://accounts-connect.internal/"),
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
				endpoints: [endpoint("connect", "CONNECT", value)],
			}),
		).toThrow();
	});

	it("fails closed when a server route has no Connect binding", () => {
		expect(() =>
			requireAccountsConnect({ currentModule: "saas", endpoints: [] }),
		).toThrow(/Codefly accounts\/connect endpoint/);
	});
});
