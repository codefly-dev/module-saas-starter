import type { FrontendServiceAllowlist } from "@codefly/saas-plugin-contract";
import { load } from "js-yaml";

const GENERATED_SOURCE =
	"deployment/topology.bindings.codefly.yaml and services/frontend/code/server/plugin-service-allowlist.generated.json";
const CODEFLY_ID = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

export interface PluginServiceDependency {
	name: string;
	module: string;
	endpoints: Array<{ name: "connect" | "rest" }>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function assertDependency(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend plugin Codefly dependencies: ${message}`);
}

function exactKeys(
	value: Record<string, unknown>,
	expected: readonly string[],
	context: string,
) {
	const actual = Object.keys(value).sort();
	const wanted = [...expected].sort();
	assertDependency(
		JSON.stringify(actual) === JSON.stringify(wanted),
		`${context} must contain only ${wanted.join(", ")}`,
	);
}

export function expectedPluginServiceDependencies(
	allowlist: FrontendServiceAllowlist,
): PluginServiceDependency[] {
	const targets = new Map<string, Set<"connect" | "rest">>();
	for (const entry of allowlist.entries) {
		const key = `${entry.target.module}/${entry.target.service}`;
		const endpoints = targets.get(key) ?? new Set<"connect" | "rest">();
		endpoints.add(entry.target.endpoint);
		targets.set(key, endpoints);
	}

	return [...targets]
		.sort(([left], [right]) => left.localeCompare(right))
		.map(([key, endpoints]) => {
			const separator = key.indexOf("/");
			return {
				name: key.slice(separator + 1),
				module: key.slice(0, separator),
				endpoints: [...endpoints].sort().map((name) => ({ name })),
			};
		});
}

function readExternalDependencies(
	manifestText: string,
): PluginServiceDependency[] {
	assertDependency(
		manifestText.startsWith(
			`# Code generated from ${GENERATED_SOURCE}. DO NOT EDIT.\n`,
		),
		"frontend service manifest was not generated from the topology and plugin allowlist",
	);
	const manifest = load(manifestText);
	assertDependency(
		isRecord(manifest),
		"frontend service manifest must be a YAML object",
	);
	assertDependency(
		manifest.name === "frontend",
		"service manifest name must be 'frontend'",
	);
	const dependencies = manifest["service-dependencies"] ?? [];
	assertDependency(
		Array.isArray(dependencies),
		"service-dependencies must be an array",
	);

	const external: PluginServiceDependency[] = [];
	const owners = new Set<string>();
	for (const raw of dependencies) {
		assertDependency(
			isRecord(raw),
			"each service dependency must be an object",
		);
		if (!("module" in raw)) continue;
		exactKeys(
			raw,
			["name", "module", "endpoints"],
			"external service dependency",
		);
		assertDependency(
			typeof raw.module === "string" && CODEFLY_ID.test(raw.module),
			`external dependency module '${String(raw.module)}' is unsafe`,
		);
		assertDependency(
			typeof raw.name === "string" && CODEFLY_ID.test(raw.name),
			`external dependency service '${String(raw.name)}' is unsafe`,
		);
		assertDependency(
			Array.isArray(raw.endpoints) && raw.endpoints.length > 0,
			"external dependency endpoints must be a non-empty array",
		);

		const owner = `${raw.module}/${raw.name}`;
		assertDependency(
			!owners.has(owner),
			`external dependency '${owner}' is duplicated`,
		);
		owners.add(owner);
		const endpointNames = raw.endpoints.map((endpoint, index) => {
			assertDependency(
				isRecord(endpoint),
				`external dependency '${owner}' endpoint ${index} must be an object`,
			);
			exactKeys(
				endpoint,
				["name"],
				`external dependency '${owner}' endpoint ${index}`,
			);
			assertDependency(
				endpoint.name === "connect" || endpoint.name === "rest",
				`external dependency '${owner}' endpoint '${String(endpoint.name)}' is unsupported`,
			);
			return endpoint.name;
		});
		assertDependency(
			endpointNames.every(
				(name, index) => index === 0 || name > endpointNames[index - 1],
			),
			`external dependency '${owner}' endpoints must be unique and sorted`,
		);
		external.push({
			name: raw.name,
			module: raw.module,
			endpoints: endpointNames.map((name) => ({ name })),
		});
	}
	return external;
}

export function assertPluginServiceDependenciesCurrent(
	allowlist: FrontendServiceAllowlist,
	manifestText: string,
): void {
	const expected = expectedPluginServiceDependencies(allowlist);
	const actual = readExternalDependencies(manifestText);
	assertDependency(
		JSON.stringify(actual) === JSON.stringify(expected),
		"frontend service manifest does not exactly match the generated plugin allowlist; run npm run generate:plugin-codefly-dependencies",
	);
}
