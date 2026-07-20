import {
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendServiceAllowlist,
	type FrontendServiceAllowlistEntry,
	type FrontendServiceBinding,
	type InstalledFrontendService,
} from "./contracts.js";

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const CODEFLY_ID = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

function assertAllowlist(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new Error(`Invalid frontend service allowlist: ${message}`);
}

function assertExactKeys(
	value: object,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertAllowlist(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function bindingKey(plugin: string, alias: string): string {
	return `${plugin}/${alias}`;
}

function validateBinding(binding: FrontendServiceBinding): void {
	assertAllowlist(
		binding !== null && typeof binding === "object" && !Array.isArray(binding),
		"binding must be an object",
	);
	assertExactKeys(binding, ["plugin", "alias", "target"], "binding");
	assertAllowlist(
		typeof binding.plugin === "string" && LOGICAL_ID.test(binding.plugin),
		`binding plugin '${String(binding.plugin)}' is unsafe`,
	);
	assertAllowlist(
		typeof binding.alias === "string" && LOGICAL_ID.test(binding.alias),
		`binding alias '${String(binding.alias)}' is unsafe`,
	);
	assertAllowlist(
		binding.target !== null &&
			typeof binding.target === "object" &&
			!Array.isArray(binding.target),
		`binding '${bindingKey(binding.plugin, binding.alias)}' target must be an object`,
	);
	assertExactKeys(
		binding.target,
		["module", "service"],
		`binding '${bindingKey(binding.plugin, binding.alias)}' target`,
	);
	assertAllowlist(
		typeof binding.target.module === "string" &&
			CODEFLY_ID.test(binding.target.module),
		`binding '${bindingKey(binding.plugin, binding.alias)}' module '${String(binding.target.module)}' is unsafe`,
	);
	assertAllowlist(
		typeof binding.target.service === "string" &&
			CODEFLY_ID.test(binding.target.service),
		`binding '${bindingKey(binding.plugin, binding.alias)}' service '${String(binding.target.service)}' is unsafe`,
	);
}

/**
 * Converges validated plugin requirements with application-owned logical
 * Codefly targets. It cannot express a hostname, URL, credential, or endpoint
 * protocol other than the one declared by the plugin requirement.
 */
export function buildFrontendServiceAllowlist(
	services: readonly InstalledFrontendService[],
	bindings: readonly FrontendServiceBinding[],
): FrontendServiceAllowlist {
	assertAllowlist(
		Array.isArray(services),
		"installed services must be an array",
	);
	assertAllowlist(Array.isArray(bindings), "bindings must be an array");

	const servicesByKey = new Map<string, InstalledFrontendService>();
	for (const service of services) {
		const key = bindingKey(service.plugin, service.alias);
		assertAllowlist(
			!servicesByKey.has(key),
			`installed service '${key}' is duplicated`,
		);
		servicesByKey.set(key, service);
	}

	const bindingsByKey = new Map<string, FrontendServiceBinding>();
	for (const binding of bindings) {
		validateBinding(binding);
		const key = bindingKey(binding.plugin, binding.alias);
		assertAllowlist(!bindingsByKey.has(key), `binding '${key}' is duplicated`);
		assertAllowlist(
			servicesByKey.has(key),
			`binding '${key}' does not match an installed service requirement`,
		);
		bindingsByKey.set(key, binding);
	}

	const entries: FrontendServiceAllowlistEntry[] = [...servicesByKey]
		.sort(([left], [right]) => left.localeCompare(right))
		.map(([key, requirement]) => {
			const binding = bindingsByKey.get(key);
			assertAllowlist(
				binding,
				`installed service '${key}' has no application binding`,
			);
			return Object.freeze({
				...requirement,
				compatibility: Object.freeze({ ...requirement.compatibility }),
				target: Object.freeze({
					module: binding.target.module,
					service: binding.target.service,
					endpoint: requirement.protocol,
				}),
			});
		});

	return Object.freeze({
		schemaVersion: 1 as const,
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		entries: Object.freeze(entries),
	});
}
