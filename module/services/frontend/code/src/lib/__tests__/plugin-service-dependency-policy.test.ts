import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendServiceAllowlist,
	type FrontendServiceAllowlistEntry,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";

import {
	assertPluginServiceDependenciesCurrent,
	expectedPluginServiceDependencies,
} from "../../../server/plugin-service-dependency-policy";

const codeDir = join(dirname(fileURLToPath(import.meta.url)), "../../..");
const header =
	"# Code generated from deployment/topology.bindings.codefly.yaml and services/frontend/code/server/plugin-service-allowlist.generated.json. DO NOT EDIT.\n";

function entry(
	plugin: string,
	alias: string,
	protocol: "connect" | "rest",
	module: string,
	service: string,
): FrontendServiceAllowlistEntry {
	return {
		plugin,
		alias,
		protocol,
		routePrefix: `/api/v1/${plugin}/${alias}`,
		compatibility: { contract: plugin, major: 1 },
		target: { module, service, endpoint: protocol },
	};
}

function allowlist(
	entries: FrontendServiceAllowlistEntry[],
): FrontendServiceAllowlist {
	return {
		schemaVersion: 1,
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		entries,
	};
}

function manifest(external = ""): string {
	return `${header}name: frontend
version: 0.0.0
agent:
  kind: codefly:service
  name: nextjs
  version: 0.0.109
  publisher: codefly.dev
service-dependencies:
  - name: accounts
    endpoints:
      - name: rest
${external}`;
}

describe("frontend plugin Codefly dependency policy", () => {
	it("keeps the canonical empty allowlist and checked-in manifest converged", () => {
		const generatedAllowlist = JSON.parse(
			readFileSync(
				join(codeDir, "server/plugin-service-allowlist.generated.json"),
				"utf8",
			),
		) as FrontendServiceAllowlist;
		const checkedInManifest = readFileSync(
			join(codeDir, "../service.codefly.yaml"),
			"utf8",
		);

		expect(() =>
			assertPluginServiceDependenciesCurrent(
				generatedAllowlist,
				checkedInManifest,
			),
		).not.toThrow();
	});

	it("groups aliases by logical target and sorts targets and endpoints", () => {
		const generated = allowlist([
			entry("observability", "rest-api", "rest", "products", "telemetry"),
			entry("audit", "api", "rest", "extensions", "audit"),
			entry("observability", "connect-api", "connect", "products", "telemetry"),
		]);

		expect(expectedPluginServiceDependencies(generated)).toEqual([
			{ module: "extensions", name: "audit", endpoints: [{ name: "rest" }] },
			{
				module: "products",
				name: "telemetry",
				endpoints: [{ name: "connect" }, { name: "rest" }],
			},
		]);
		expect(() =>
			assertPluginServiceDependenciesCurrent(
				generated,
				manifest(`  - name: audit
    module: extensions
    endpoints:
      - name: rest
  - name: telemetry
    module: products
    endpoints:
      - name: connect
      - name: rest
`),
			),
		).not.toThrow();
	});

	it("rejects stale, extra, duplicate, and hand-authored external dependencies", () => {
		const generated = allowlist([
			entry("example", "api", "rest", "products", "example"),
		]);
		expect(() =>
			assertPluginServiceDependenciesCurrent(generated, manifest()),
		).toThrow(/does not exactly match/);
		expect(() =>
			assertPluginServiceDependenciesCurrent(
				allowlist([]),
				manifest(`  - name: unexpected
    module: products
    endpoints:
      - name: rest
`),
			),
		).toThrow(/does not exactly match/);
		expect(() =>
			assertPluginServiceDependenciesCurrent(
				generated,
				manifest(`  - name: example
    module: products
    endpoints:
      - name: rest
  - name: example
    module: products
    endpoints:
      - name: rest
`),
			),
		).toThrow(/duplicated/);
		expect(() =>
			assertPluginServiceDependenciesCurrent(
				allowlist([]),
				manifest().replace(header, "# Hand-authored.\n"),
			),
		).toThrow(/was not generated/);
	});
});
