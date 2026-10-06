import { describe, expect, it } from "vitest";

import {
	type CodeflyRuntimeReader,
	resolveVerifiedPublicOrigin,
} from "@/lib/codefly-gateway-context";

// R1019-N12: one origin must have one spelling on BOTH sides of the comparison.
//
// This side canonicalizes with `new URL(...).origin`; the accounts service reproduces
// the same answers in Go. Where the two disagreed, a configured origin and the
// equivalent origin resolved here compared unequal, and the refusal read as a
// misconfiguration.
//
// The same table is asserted in pkg/auth/public_origin_vectors_test.go, and
// module/tools/public_origin_vectors_lockstep_test.go holds the two copies identical,
// so a case fixed on one side cannot be left failing on the other.
const canonicalPublicOriginVectors: [string, string][] = [
	["https://app.example", "https://app.example"],
	["https://APP.example", "https://app.example"],
	["https://app.example:443", "https://app.example"],
	["https://app.example:0443", "https://app.example"],
	["https://app.example:", "https://app.example"],
	["https://app.example:8443", "https://app.example:8443"],
	["https://app.example:08443", "https://app.example:8443"],
	["https://app.example/", "https://app.example"],
	["https://[0:0:0:0:0:0:0:1]", "https://[::1]"],
	["https://[::1]:443", "https://[::1]"],
	["https://bücher.example", "https://xn--bcher-kva.example"],
	["http://localhost:80", "http://localhost"],
	["http://127.0.0.1:3000", "http://127.0.0.1:3000"],
	["http://[::1]:80", "http://[::1]"],
];

// Refused on both sides. Port 0 is the one case the parser itself accepts; see the
// note on canonicalOrigin for why both sides refuse it anyway.
const refusedPublicOriginVectors: string[] = [
	"https://:443",
	"https://app.example:99999",
	"https://app.example:0",
	"https://app.example:-1",
	"https://app.example/path",
	"https://app.example?q=1",
	"https://app.example#f",
	"https://user:secret@app.example",
	"app.example",
	"",
];

// A deployed build with the origin in `application/APP_BASE_URL` and nothing else to
// fall back on: the configured value is the only input, so the result is the
// canonicalization and nothing about the resolution order.
function configuredWith(appBaseUrl: string): CodeflyRuntimeReader {
	return {
		currentModule: () => "saas-starter",
		currentService: () => "frontend",
		endpoints: () => [],
		workspaceSecret: () => "internal-test-token",
		workspaceConfiguration: (group: string, key: string) =>
			group === "application" && key === "APP_BASE_URL" ? appBaseUrl : undefined,
		isDeployedBuild: () => true,
	};
}

describe("the verified public origin has one spelling", () => {
	it.each(canonicalPublicOriginVectors)("%s canonicalizes to %s", (input, expected) => {
		expect(resolveVerifiedPublicOrigin(configuredWith(input))).toBe(expected);
	});

	it.each(refusedPublicOriginVectors.map((input) => [input]))(
		"refuses %s rather than guessing an origin",
		(input) => {
			expect(resolveVerifiedPublicOrigin(configuredWith(input))).toBeUndefined();
		},
	);
});
