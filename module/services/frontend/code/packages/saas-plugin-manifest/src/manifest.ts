import {
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	type FrontendPlugin,
	validateFrontendPlugins,
} from "@codefly/saas-plugin-contract";
import {
	type ApiConsume,
	type ApiExpose,
	type ApiProtocol,
	type CapabilityRequirement,
	type EventPublication,
	type EventSubscription,
	PLUGIN_MANIFEST_API_VERSION,
	PLUGIN_MANIFEST_KIND,
	type PluginConfigKey,
	type PluginEgress,
	type PluginEntitlement,
	type PluginIdentity,
	type PluginLifecycle,
	type PluginManifest,
	type PluginMigration,
	type PluginPermission,
	type PluginService,
} from "./contracts.js";

function assertManifest(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition) throw new Error(`Invalid plugin manifest: ${message}`);
}

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const NAMESPACED_ID = /^[a-z][a-z0-9._-]*(?::[a-z][a-z0-9._-]*)*$/;
const SEMVER =
	/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-.]+)?(?:\+[0-9A-Za-z-.]+)?$/;
const EVENT_TYPE = /^[a-z][a-z0-9]*(?:\.[a-z0-9]+)*\.v[1-9][0-9]*$/;
const CONFIG_KEY = /^[A-Z][A-Z0-9_]*$/;
const MIGRATION_ID = /^\d{4}_[a-z0-9]+(?:_[a-z0-9]+)*$/;
const PUBLISHER = /^(?:[a-z0-9-]+\.)+[a-z]{2,}$/;
const EGRESS_HOST =
	/^(?:\*\.)?(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const PROTOCOLS: readonly ApiProtocol[] = ["connect", "rest", "grpc"];
const CONFIG_TYPES = ["string", "int", "bool", "duration", "url"] as const;
const MIGRATION_SCOPES = ["shared", "tenant"] as const;

function isObject(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function assertExactKeys(
	value: Record<string, unknown>,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertManifest(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

function assertNonEmptyString(
	value: unknown,
	context: string,
): asserts value is string {
	assertManifest(
		typeof value === "string" && value.trim().length > 0,
		`${context} must be a non-empty string`,
	);
}

function assertOptionalDescription(value: unknown, context: string): void {
	assertManifest(
		value === undefined ||
			(typeof value === "string" && value.trim().length > 0),
		`${context} description must be a non-empty string`,
	);
}

function assertPositiveMajor(value: unknown, context: string): void {
	assertManifest(
		Number.isSafeInteger(value) && (value as number) > 0,
		`${context} major must be a positive integer`,
	);
}

function assertProtocols(value: unknown, context: string): void {
	assertManifest(
		Array.isArray(value) &&
			value.length > 0 &&
			value.every((protocol) => PROTOCOLS.includes(protocol as ApiProtocol)),
		`${context} protocols must be a non-empty set of ${PROTOCOLS.join(", ")}`,
	);
	assertUnique(
		(value as string[]).map((protocol) => ({
			value: protocol,
			owner: context,
		})),
		`${context} protocol`,
	);
}

function assertUnique(
	values: ReadonlyArray<{ value: string; owner: string }>,
	kind: string,
): void {
	const owners = new Map<string, string>();
	for (const { value, owner } of values) {
		const previous = owners.get(value);
		assertManifest(
			previous === undefined,
			`${kind} '${value}' is declared by both '${previous}' and '${owner}'`,
		);
		owners.set(value, owner);
	}
}

function assertArray(
	value: unknown,
	context: string,
): asserts value is readonly unknown[] {
	assertManifest(Array.isArray(value), `${context} must be an array`);
}

function validateIdentity(value: unknown): asserts value is PluginIdentity {
	assertManifest(isObject(value), "metadata must be an object");
	assertExactKeys(
		value,
		["name", "version", "displayName", "description", "publisher"],
		"metadata",
	);
	assertManifest(
		typeof value.name === "string" && LOGICAL_ID.test(value.name),
		`metadata name '${String(value.name)}' is not a valid logical id`,
	);
	assertManifest(
		typeof value.version === "string" && SEMVER.test(value.version),
		`metadata version '${String(value.version)}' is not semantic`,
	);
	assertManifest(
		value.displayName === undefined ||
			(typeof value.displayName === "string" &&
				value.displayName.trim().length > 0),
		"metadata displayName must be a non-empty string",
	);
	assertOptionalDescription(value.description, "metadata");
	assertManifest(
		value.publisher === undefined ||
			(typeof value.publisher === "string" && PUBLISHER.test(value.publisher)),
		`metadata publisher '${String(value.publisher)}' must be a domain`,
	);
}

function validateService(value: unknown): asserts value is PluginService {
	assertManifest(isObject(value), "service must be an object");
	assertExactKeys(value, ["name", "description", "endpoints"], "service");
	assertManifest(
		typeof value.name === "string" && LOGICAL_ID.test(value.name),
		`service name '${String(value.name)}' is not a valid logical id`,
	);
	assertOptionalDescription(
		value.description,
		`service '${String(value.name)}'`,
	);
	assertProtocols(value.endpoints, `service '${String(value.name)}' endpoints`);
}

function validateExpose(value: unknown): asserts value is ApiExpose {
	assertManifest(isObject(value), "api expose must be an object");
	assertExactKeys(value, ["contract", "major", "protocols"], "api expose");
	assertManifest(
		typeof value.contract === "string" && LOGICAL_ID.test(value.contract),
		`api expose contract '${String(value.contract)}' is not a valid id`,
	);
	assertPositiveMajor(value.major, `api expose '${String(value.contract)}'`);
	assertProtocols(
		value.protocols,
		`api expose '${String(value.contract)}' protocols`,
	);
}

function validateConsume(value: unknown): asserts value is ApiConsume {
	assertManifest(isObject(value), "api consume must be an object");
	assertExactKeys(value, ["contract", "major", "alias"], "api consume");
	assertManifest(
		typeof value.contract === "string" && LOGICAL_ID.test(value.contract),
		`api consume contract '${String(value.contract)}' is not a valid id`,
	);
	assertPositiveMajor(value.major, `api consume '${String(value.contract)}'`);
	assertManifest(
		value.alias === undefined ||
			(typeof value.alias === "string" && LOGICAL_ID.test(value.alias)),
		`api consume '${String(value.contract)}' alias is not a valid id`,
	);
}

function validatePublication(
	value: unknown,
): asserts value is EventPublication {
	assertManifest(isObject(value), "event publication must be an object");
	assertExactKeys(value, ["type", "description"], "event publication");
	assertManifest(
		typeof value.type === "string" && EVENT_TYPE.test(value.type),
		`event publication type '${String(value.type)}' must be namespaced and versioned`,
	);
	assertOptionalDescription(value.description, "event publication");
}

function validateSubscription(
	value: unknown,
): asserts value is EventSubscription {
	assertManifest(isObject(value), "event subscription must be an object");
	assertExactKeys(
		value,
		["type", "handler", "description"],
		"event subscription",
	);
	assertManifest(
		typeof value.type === "string" && EVENT_TYPE.test(value.type),
		`event subscription type '${String(value.type)}' must be namespaced and versioned`,
	);
	assertManifest(
		typeof value.handler === "string" && LOGICAL_ID.test(value.handler),
		`event subscription handler '${String(value.handler)}' is not a valid id`,
	);
	assertOptionalDescription(value.description, "event subscription");
}

function validatePermission(value: unknown): asserts value is PluginPermission {
	assertManifest(isObject(value), "permission must be an object");
	assertExactKeys(value, ["id", "description"], "permission");
	assertManifest(
		typeof value.id === "string" && NAMESPACED_ID.test(value.id),
		`permission id '${String(value.id)}' must be a namespaced id`,
	);
	assertOptionalDescription(value.description, "permission");
}

function validateEntitlement(
	value: unknown,
): asserts value is PluginEntitlement {
	assertManifest(isObject(value), "entitlement must be an object");
	assertExactKeys(
		value,
		["id", "description", "defaultGranted"],
		"entitlement",
	);
	assertManifest(
		typeof value.id === "string" && NAMESPACED_ID.test(value.id),
		`entitlement id '${String(value.id)}' must be a namespaced id`,
	);
	assertManifest(
		value.defaultGranted === undefined ||
			typeof value.defaultGranted === "boolean",
		`entitlement '${String(value.id)}' defaultGranted must be a boolean`,
	);
	assertOptionalDescription(value.description, "entitlement");
}

function validateConfigKey(value: unknown): asserts value is PluginConfigKey {
	assertManifest(isObject(value), "config key must be an object");
	assertExactKeys(
		value,
		["key", "type", "required", "secret", "description"],
		"config key",
	);
	assertManifest(
		typeof value.key === "string" && CONFIG_KEY.test(value.key),
		`config key '${String(value.key)}' must be an upper-case env key`,
	);
	assertManifest(
		CONFIG_TYPES.includes(value.type as (typeof CONFIG_TYPES)[number]),
		`config key '${String(value.key)}' has unsupported type '${String(value.type)}'`,
	);
	assertManifest(
		value.required === undefined || typeof value.required === "boolean",
		`config key '${String(value.key)}' required must be a boolean`,
	);
	assertManifest(
		value.secret === undefined || typeof value.secret === "boolean",
		`config key '${String(value.key)}' secret must be a boolean`,
	);
	assertOptionalDescription(value.description, "config key");
}

function validateMigration(value: unknown): asserts value is PluginMigration {
	assertManifest(isObject(value), "migration must be an object");
	assertExactKeys(value, ["id", "scope", "description"], "migration");
	assertManifest(
		typeof value.id === "string" && MIGRATION_ID.test(value.id),
		`migration id '${String(value.id)}' must be like '0001_init'`,
	);
	assertManifest(
		MIGRATION_SCOPES.includes(value.scope as (typeof MIGRATION_SCOPES)[number]),
		`migration '${String(value.id)}' scope '${String(value.scope)}' is unsupported`,
	);
	assertOptionalDescription(value.description, "migration");
}

function validateEgress(value: unknown): asserts value is PluginEgress {
	assertManifest(isObject(value), "egress rule must be an object");
	assertExactKeys(value, ["host", "ports", "description"], "egress rule");
	assertManifest(
		typeof value.host === "string" && EGRESS_HOST.test(value.host),
		`egress host '${String(value.host)}' is not a valid host`,
	);
	if (value.ports !== undefined) {
		assertArray(value.ports, `egress '${String(value.host)}' ports`);
		assertManifest(
			value.ports.length > 0 &&
				value.ports.every(
					(port) =>
						Number.isSafeInteger(port) &&
						(port as number) >= 1 &&
						(port as number) <= 65535,
				),
			`egress '${String(value.host)}' ports must be integers in 1..65535`,
		);
	}
	assertOptionalDescription(value.description, "egress rule");
}

function validateNeed(value: unknown): asserts value is CapabilityRequirement {
	assertManifest(isObject(value), "need must be an object");
	assertExactKeys(value, ["capability", "optionality"], "need");
	assertManifest(
		typeof value.capability === "string" &&
			NAMESPACED_ID.test(value.capability),
		`need capability '${String(value.capability)}' must be a namespaced id`,
	);
	assertManifest(
		value.optionality === undefined ||
			value.optionality === "required" ||
			value.optionality === "optional",
		`need '${String(value.capability)}' optionality is unsupported`,
	);
}

function validateLifecycleSteps(value: unknown, context: string): void {
	assertArray(value, context);
	for (const step of value) {
		assertManifest(isObject(step), `${context} step must be an object`);
		assertExactKeys(step, ["job", "description"], `${context} step`);
		assertManifest(
			typeof step.job === "string" && LOGICAL_ID.test(step.job),
			`${context} job '${String(step.job)}' is not a valid id`,
		);
		assertOptionalDescription(step.description, `${context} step`);
	}
	assertUnique(
		(value as { job: string }[]).map((step) => ({
			value: step.job,
			owner: context,
		})),
		`${context} job`,
	);
}

function validateLifecycle(value: unknown): asserts value is PluginLifecycle {
	assertManifest(isObject(value), "lifecycle must be an object");
	assertExactKeys(value, ["install", "upgrade", "uninstall"], "lifecycle");
	if (value.install !== undefined)
		validateLifecycleSteps(value.install, "lifecycle install");
	if (value.upgrade !== undefined)
		validateLifecycleSteps(value.upgrade, "lifecycle upgrade");
	if (value.uninstall !== undefined)
		validateLifecycleSteps(value.uninstall, "lifecycle uninstall");
}

function validateIntegrity(value: unknown): void {
	assertManifest(isObject(value), "integrity must be an object");
	assertExactKeys(value, ["signature", "artifacts"], "integrity");
	if (value.signature !== undefined) {
		const signature = value.signature;
		assertManifest(
			isObject(signature),
			"integrity signature must be an object",
		);
		assertExactKeys(
			signature,
			["algorithm", "keyId", "value"],
			"integrity signature",
		);
		assertNonEmptyString(signature.algorithm, "integrity signature algorithm");
		assertNonEmptyString(signature.keyId, "integrity signature keyId");
		assertNonEmptyString(signature.value, "integrity signature value");
	}
	if (value.artifacts !== undefined) {
		assertArray(value.artifacts, "integrity artifacts");
		for (const artifact of value.artifacts) {
			assertManifest(
				isObject(artifact),
				"integrity artifact must be an object",
			);
			assertExactKeys(artifact, ["path", "sha256"], "integrity artifact");
			assertNonEmptyString(artifact.path, "integrity artifact path");
			assertManifest(
				typeof artifact.sha256 === "string" && SHA256.test(artifact.sha256),
				`integrity artifact '${String(artifact.path)}' sha256 must be 64 hex chars`,
			);
		}
		assertUnique(
			(value.artifacts as { path: string }[]).map((artifact) => ({
				value: artifact.path,
				owner: "integrity",
			})),
			"integrity artifact path",
		);
	}
}

function validateArraySection<T>(
	value: unknown,
	context: string,
	validate: (item: unknown) => void,
	uniqueKey: (item: T) => string | undefined,
	uniqueKind: string,
): void {
	assertArray(value, context);
	for (const item of value) validate(item);
	const keyed = (value as T[])
		.map((item) => uniqueKey(item))
		.filter((key): key is string => key !== undefined)
		.map((key) => ({ value: key, owner: uniqueKind }));
	assertUnique(keyed, uniqueKind);
}

function validateUi(manifest: Record<string, unknown>, name: string): void {
	const ui = manifest.ui;
	assertManifest(isObject(ui), "ui must be an object");
	assertExactKeys(
		ui,
		["navigation", "navItems", "routes", "widgets", "services"],
		"ui",
	);
	const frontendPlugin = {
		contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
		name,
		...ui,
	} as FrontendPlugin;
	validateFrontendPlugins([frontendPlugin]);
}

/**
 * Validates a parsed `plugin.codefly.yaml` document. The manifest is JSON-safe:
 * it declares stable ids and compatibility metadata only, never deployment
 * addresses, credentials, or resolved bindings. The frontend contribution
 * block is validated by the frontend contract so the two never fork.
 */
export function assertPluginManifest(
	value: unknown,
): asserts value is PluginManifest {
	assertManifest(isObject(value), "manifest must be an object");
	assertExactKeys(
		value,
		[
			"apiVersion",
			"kind",
			"metadata",
			"services",
			"api",
			"events",
			"ui",
			"needs",
			"permissions",
			"entitlements",
			"config",
			"migrations",
			"egress",
			"lifecycle",
			"integrity",
		],
		"manifest",
	);
	assertManifest(
		value.apiVersion === PLUGIN_MANIFEST_API_VERSION,
		`manifest apiVersion '${String(value.apiVersion)}'; expected '${PLUGIN_MANIFEST_API_VERSION}'`,
	);
	assertManifest(
		value.kind === PLUGIN_MANIFEST_KIND,
		`manifest kind '${String(value.kind)}'; expected '${PLUGIN_MANIFEST_KIND}'`,
	);
	validateIdentity(value.metadata);
	const name = value.metadata.name;

	if (value.services !== undefined) {
		validateArraySection<PluginService>(
			value.services,
			"services",
			validateService,
			(service) => service.name,
			"service name",
		);
	}

	if (value.api !== undefined) {
		assertManifest(isObject(value.api), "api must be an object");
		assertExactKeys(value.api, ["exposes", "consumes"], "api");
		if (value.api.exposes !== undefined) {
			validateArraySection<ApiExpose>(
				value.api.exposes,
				"api exposes",
				validateExpose,
				(expose) => expose.contract,
				"api expose contract",
			);
		}
		if (value.api.consumes !== undefined) {
			validateArraySection<ApiConsume>(
				value.api.consumes,
				"api consumes",
				validateConsume,
				(consume) => consume.contract,
				"api consume contract",
			);
		}
	}

	if (value.events !== undefined) {
		assertManifest(isObject(value.events), "events must be an object");
		assertExactKeys(value.events, ["publishes", "subscribes"], "events");
		if (value.events.publishes !== undefined) {
			validateArraySection<EventPublication>(
				value.events.publishes,
				"events publishes",
				validatePublication,
				(event) => event.type,
				"published event type",
			);
		}
		if (value.events.subscribes !== undefined) {
			validateArraySection<EventSubscription>(
				value.events.subscribes,
				"events subscribes",
				validateSubscription,
				(event) => event.handler,
				"event subscription handler",
			);
		}
	}

	if (value.ui !== undefined) validateUi(value, name);

	if (value.needs !== undefined) {
		validateArraySection<CapabilityRequirement>(
			value.needs,
			"needs",
			validateNeed,
			(need) => need.capability,
			"needed capability",
		);
	}

	if (value.permissions !== undefined) {
		validateArraySection<PluginPermission>(
			value.permissions,
			"permissions",
			validatePermission,
			(permission) => permission.id,
			"permission id",
		);
	}

	if (value.entitlements !== undefined) {
		validateArraySection<PluginEntitlement>(
			value.entitlements,
			"entitlements",
			validateEntitlement,
			(entitlement) => entitlement.id,
			"entitlement id",
		);
	}

	if (value.config !== undefined) {
		validateArraySection<PluginConfigKey>(
			value.config,
			"config",
			validateConfigKey,
			(config) => config.key,
			"config key",
		);
	}

	if (value.migrations !== undefined) {
		validateArraySection<PluginMigration>(
			value.migrations,
			"migrations",
			validateMigration,
			(migration) => migration.id,
			"migration id",
		);
	}

	if (value.egress !== undefined) {
		validateArraySection<PluginEgress>(
			value.egress,
			"egress",
			validateEgress,
			(rule) => rule.host,
			"egress host",
		);
	}

	if (value.lifecycle !== undefined) validateLifecycle(value.lifecycle);
	if (value.integrity !== undefined) validateIntegrity(value.integrity);
}

/**
 * Validates a parsed manifest document and returns it typed. Use at every
 * boundary that reads a `plugin.codefly.yaml` from disk or a registry.
 */
export function loadPluginManifest(value: unknown): PluginManifest {
	assertPluginManifest(value);
	return value;
}

/**
 * Defines and validates a manifest authored in TypeScript while preserving its
 * literal ids, contracts, and compatibility metadata.
 */
export function definePluginManifest<const Manifest extends PluginManifest>(
	manifest: Manifest,
): Manifest {
	assertPluginManifest(manifest);
	return manifest;
}
