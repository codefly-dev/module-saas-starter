/**
 * Holds the forwarded host procedures to this app's own call sites.
 *
 * The measured failure this exists for: a page called
 * `host.session.v1.SessionService/Current` through `apiTransport`, the proxy's
 * allowlist did not carry it, so the call never left this app — it fell through
 * to the Next router and answered a 404 page. Nothing failed to build, no
 * service logged anything, and because an unconfirmed session is never
 * presented as the person, every administrator action was withheld from
 * everyone holding them.
 *
 * A feature test cannot catch that class: it stubs the transport, so it proves
 * the client calls the path, never that the path is routed. This test reads the
 * host clients out of the source instead, so adding a call site is what adds
 * the route, and forgetting is a red suite rather than a 404 in a browser.
 *
 * It reads this app's own `src/` and stops there — the chrome, which is where
 * the regression was. `<SolutionSetup>` in `packages/saas-ui` reaches the same
 * procedures through a client factory whose calls are nested property accesses,
 * which no regex settles, so the one direction asserted here is that every
 * procedure this app calls has a route. A listed procedure with no call site in
 * `src/` is not a finding: it may be one the Setup tab calls.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { HOST_BROWSER_PROCEDURES } from "@/lib/host-browser-procedures";

const SOURCE_ROOT = join(__dirname, "..", "..");
const GENERATED_ROOT = join(SOURCE_ROOT, "gen");

/** Every source file of this app, excluding tests and generated output. */
function sourceFiles(directory: string): string[] {
	const found: string[] = [];
	for (const entry of readdirSync(directory)) {
		const path = join(directory, entry);
		if (statSync(path).isDirectory()) {
			if (entry === "gen" || entry === "__tests__" || entry === "test")
				continue;
			found.push(...sourceFiles(path));
			continue;
		}
		if (!/\.tsx?$/.test(entry) || /\.test\.tsx?$/.test(entry)) continue;
		found.push(path);
	}
	return found;
}

/**
 * The procedure each method of a generated host service belongs to, keyed by
 * the TypeScript name a client calls it by.
 *
 * Read out of the generated file's own `@generated from rpc` lines rather than
 * by importing the descriptor, so the lookup needs no module resolution and
 * says exactly what protoc-gen-es wrote.
 */
function proceduresOf(generatedModule: string): Map<string, string> {
	const path = join(GENERATED_ROOT, `${generatedModule}.ts`);
	const source = readFileSync(path, "utf8");
	const procedures = new Map<string, string>();
	const rpcs = source.matchAll(
		/@generated from rpc ([\w.]+)\.(\w+)\s*\*\/\s*(\w+):/g,
	);
	for (const [, service, method, localName] of rpcs) {
		procedures.set(localName, `/${service}/${method}`);
	}
	return procedures;
}

/** Every host procedure this app's source calls through a Connect client. */
function calledProcedures(): Map<string, string> {
	const called = new Map<string, string>();
	for (const file of sourceFiles(SOURCE_ROOT)) {
		const source = readFileSync(file, "utf8");
		// Which identifiers in this file name a host service, and from where.
		const hostServices = new Map<string, string>();
		const imports = source.matchAll(
			/import\s*(?:type\s*)?\{([^}]+)\}\s*from\s*"@\/gen\/(host\/[\w/]+)"/g,
		);
		for (const [, named, generatedModule] of imports) {
			for (const binding of named.split(",")) {
				const name = binding
					.replace(/\btype\b/, "")
					.trim()
					.split(/\s+as\s+/);
				const identifier = (name[1] ?? name[0]).trim();
				if (identifier) hostServices.set(identifier, generatedModule);
			}
		}
		if (hostServices.size === 0) continue;
		// Which variables hold a client of one of them.
		for (const [, variable, service] of source.matchAll(
			/(?:const|let)\s+(\w+)\s*=\s*createClient\(\s*(\w+)\s*,/g,
		)) {
			const generatedModule = hostServices.get(service);
			if (!generatedModule) continue;
			const procedures = proceduresOf(generatedModule);
			// Every method called on it. A name the service does not declare is
			// some other value of the same name (`installations.map`), not a call.
			for (const [, method] of source.matchAll(
				new RegExp(`\\b${variable}\\s*\\.\\s*(\\w+)\\s*\\(`, "g"),
			)) {
				const procedure = procedures.get(method);
				if (procedure) called.set(procedure, `${file} (${variable}.${method})`);
			}
		}
	}
	return called;
}

describe("the host procedures this app forwards", () => {
	it("finds the host clients it is meant to read", () => {
		// A regex that stops matching would make every assertion below vacuous,
		// so the scan has to find something before it can prove anything.
		const called = calledProcedures();
		expect(called.size).toBeGreaterThan(3);
		expect([...called.keys()]).toContain(
			"/host.session.v1.SessionService/Current",
		);
	});

	it("carries every host procedure a page of this app calls", () => {
		const forwarded = new Set<string>(HOST_BROWSER_PROCEDURES);
		const missing = [...calledProcedures()]
			.filter(([procedure]) => !forwarded.has(procedure))
			.map(([procedure, site]) => `${procedure} called from ${site}`);
		// Each of these answers a 404 page from this app instead of reaching the
		// gateway. Add it to HOST_BROWSER_PROCEDURES, or stop calling it from a
		// browser.
		expect(missing).toEqual([]);
	});
});
