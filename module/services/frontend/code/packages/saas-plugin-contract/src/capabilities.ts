import { create, fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";

import type { FrontendServiceCompatibility } from "./contracts.js";
import {
	FrontendPluginCapabilityService,
	GetFrontendPluginCapabilitiesRequestSchema,
	GetFrontendPluginCapabilitiesResponseSchema,
	type GetFrontendPluginCapabilitiesResponse,
} from "./gen/saas/frontend/plugin/v1/capabilities_pb.js";

export type {
	GetFrontendPluginCapabilitiesRequest,
	GetFrontendPluginCapabilitiesResponse,
} from "./gen/saas/frontend/plugin/v1/capabilities_pb.js";
export {
	FrontendPluginCapabilityService,
	GetFrontendPluginCapabilitiesRequestSchema,
	GetFrontendPluginCapabilitiesResponseSchema,
} from "./gen/saas/frontend/plugin/v1/capabilities_pb.js";

export const FRONTEND_PLUGIN_CAPABILITIES_SCHEMA_VERSION = 1 as const;
export const FRONTEND_PLUGIN_CAPABILITIES_REST_PATH =
	"/.well-known/codefly/frontend-plugin-capabilities" as const;
export const FRONTEND_PLUGIN_CAPABILITIES_CONNECT_PROCEDURE =
	`/${FrontendPluginCapabilityService.typeName}/${FrontendPluginCapabilityService.method.getFrontendPluginCapabilities.name}` as const;
export const FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH = Object.freeze([
	".well-known" as const,
	"capabilities" as const,
]);

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const MAX_LOGICAL_ID_LENGTH = 128;
const MAX_CAPABILITIES = 128;
const MAX_UINT32 = 4_294_967_295;

export interface FrontendPluginCapabilitiesInput {
	contract: string;
	contractMajor: number;
	capabilities?: readonly string[];
}

function assertCapabilities(
	condition: unknown,
	message: string,
): asserts condition {
	if (!condition)
		throw new TypeError(`Invalid frontend plugin capabilities: ${message}`);
}

function validateLogicalID(
	value: unknown,
	kind: string,
): asserts value is string {
	assertCapabilities(
		typeof value === "string" &&
			value.length <= MAX_LOGICAL_ID_LENGTH &&
			LOGICAL_ID.test(value),
		`${kind} '${String(value)}' is unsafe`,
	);
}

function normalizeCapabilities(
	message: GetFrontendPluginCapabilitiesResponse,
): GetFrontendPluginCapabilitiesResponse {
	assertCapabilities(
		message.schemaVersion === FRONTEND_PLUGIN_CAPABILITIES_SCHEMA_VERSION,
		`unsupported schema version '${message.schemaVersion}'`,
	);
	validateLogicalID(message.contract, "contract");
	assertCapabilities(
		Number.isSafeInteger(message.contractMajor) &&
			message.contractMajor > 0 &&
			message.contractMajor <= MAX_UINT32,
		"contract major must be a positive integer",
	);
	assertCapabilities(
		message.capabilities.length <= MAX_CAPABILITIES,
		`capability count exceeds ${MAX_CAPABILITIES}`,
	);
	for (const capability of message.capabilities)
		validateLogicalID(capability, "capability");
	const capabilities = [...message.capabilities].sort();
	assertCapabilities(
		new Set(capabilities).size === capabilities.length,
		"capabilities must be unique",
	);
	const normalized = create(GetFrontendPluginCapabilitiesResponseSchema, {
		schemaVersion: message.schemaVersion,
		contract: message.contract,
		contractMajor: message.contractMajor,
		capabilities,
	});
	Object.freeze(normalized.capabilities);
	return Object.freeze(normalized);
}

/** Build the canonical ProtoJSON-compatible response in backend implementations. */
export function defineFrontendPluginCapabilities(
	input: FrontendPluginCapabilitiesInput,
): GetFrontendPluginCapabilitiesResponse {
	return normalizeCapabilities(
		create(GetFrontendPluginCapabilitiesResponseSchema, {
			schemaVersion: FRONTEND_PLUGIN_CAPABILITIES_SCHEMA_VERSION,
			contract: input.contract,
			contractMajor: input.contractMajor,
			capabilities: [...(input.capabilities ?? [])],
		}),
	);
}

/** Parse strict ProtoJSON and apply the semantic capability invariants. */
export function parseFrontendPluginCapabilities(
	value: JsonValue,
): GetFrontendPluginCapabilitiesResponse {
	return normalizeCapabilities(
		fromJson(GetFrontendPluginCapabilitiesResponseSchema, value, {
			ignoreUnknownFields: false,
		}),
	);
}

/** Serialize a validated response using protobuf JSON field rules. */
export function frontendPluginCapabilitiesToJson(
	capabilities: GetFrontendPluginCapabilitiesResponse,
): JsonValue {
	return toJson(
		GetFrontendPluginCapabilitiesResponseSchema,
		normalizeCapabilities(capabilities),
	);
}

export function supportsFrontendPluginContract(
	capabilities: GetFrontendPluginCapabilitiesResponse,
	requirement: FrontendServiceCompatibility,
): boolean {
	return (
		capabilities.contract === requirement.contract &&
		capabilities.contractMajor === requirement.major
	);
}

export function emptyFrontendPluginCapabilitiesRequest() {
	return create(GetFrontendPluginCapabilitiesRequestSchema);
}
