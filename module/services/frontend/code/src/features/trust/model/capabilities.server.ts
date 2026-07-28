import "server-only";

import { readFileSync } from "node:fs";

import rawManifest from "../capability-manifest.json";
import {
	type CapabilityContext,
	type CapabilityContextDocument,
	type CapabilityEvidence,
	type CapabilityManifest,
	publicCapabilities,
	starterDefaultCapabilityContext,
} from "./capabilities";

const CONTEXT_PATH_ENV = "TRUST_CAPABILITY_CONTEXT_FILE";
const capabilityManifest = rawManifest as CapabilityManifest;

export function loadPublicCapabilities(now = new Date()) {
	return publicCapabilities(capabilityManifest, loadCapabilityContext(now));
}

export function loadCapabilityContext(now = new Date()): CapabilityContext {
	const path = process.env[CONTEXT_PATH_ENV]?.trim();
	if (!path) return starterDefaultCapabilityContext(now);

	const document = parseCapabilityContext(
		JSON.parse(readFileSync(path, "utf8")) as unknown,
		capabilityManifest,
	);
	return { ...document, now };
}

export function parseCapabilityContext(
	value: unknown,
	manifest: CapabilityManifest,
): CapabilityContextDocument {
	if (!isRecord(value)) {
		throw new Error("capability context must be a JSON object");
	}
	const allowedFields = new Set([
		"schemaVersion",
		"environment",
		"scope",
		"configuredProviders",
		"configuredSettings",
		"evidence",
	]);
	rejectUnknownFields(value, allowedFields, "capability context");
	if (value.schemaVersion !== 1) {
		throw new Error("capability context schemaVersion must be 1");
	}
	const environment = requiredString(value.environment, "environment");
	const scope = requiredString(value.scope, "scope");
	const configuredProviders = stringArray(
		value.configuredProviders,
		"configuredProviders",
	);
	const configuredSettings = stringArray(
		value.configuredSettings,
		"configuredSettings",
	);
	if (!Array.isArray(value.evidence)) {
		throw new Error("capability context evidence must be an array");
	}
	const capabilityIDs = new Set(
		manifest.capabilities.map((capability) => capability.id),
	);
	const evidenceIDs = new Set<string>();
	const evidence = value.evidence.map((record, index) =>
		parseEvidence(record, index, capabilityIDs, evidenceIDs),
	);
	return {
		schemaVersion: 1,
		environment,
		scope,
		configuredProviders,
		configuredSettings,
		evidence,
	};
}

function parseEvidence(
	value: unknown,
	index: number,
	capabilityIDs: Set<string>,
	evidenceIDs: Set<string>,
): CapabilityEvidence {
	const prefix = `evidence[${index}]`;
	if (!isRecord(value)) throw new Error(`${prefix} must be an object`);
	const allowedFields = new Set([
		"id",
		"capabilityId",
		"environment",
		"scope",
		"owner",
		"verifier",
		"source",
		"performedAt",
		"expiresAt",
		"reviewAt",
		"status",
		"state",
		"visibility",
		"publicSummary",
	]);
	rejectUnknownFields(value, allowedFields, prefix);
	const id = requiredString(value.id, `${prefix}.id`);
	if (evidenceIDs.has(id)) throw new Error(`${prefix}.id duplicates ${id}`);
	evidenceIDs.add(id);
	const capabilityId = requiredString(
		value.capabilityId,
		`${prefix}.capabilityId`,
	);
	if (!capabilityIDs.has(capabilityId)) {
		throw new Error(
			`${prefix}.capabilityId does not reference a declared capability`,
		);
	}
	const state = enumValue(
		value.state,
		["operationally_verified", "externally_attested"] as const,
		`${prefix}.state`,
	);
	const status = enumValue(
		value.status,
		["current", "expired", "revoked", "rejected"] as const,
		`${prefix}.status`,
	);
	const visibility = enumValue(
		value.visibility,
		["private", "public_summary"] as const,
		`${prefix}.visibility`,
	);
	const publicSummary =
		value.publicSummary === undefined
			? undefined
			: requiredString(value.publicSummary, `${prefix}.publicSummary`);
	if (visibility === "public_summary" && publicSummary === undefined) {
		throw new Error(`${prefix}.publicSummary is required`);
	}
	const performedAt = timestamp(value.performedAt, `${prefix}.performedAt`);
	const reviewAt = timestamp(value.reviewAt, `${prefix}.reviewAt`);
	const expiresAt =
		value.expiresAt === undefined
			? undefined
			: timestamp(value.expiresAt, `${prefix}.expiresAt`);
	if (Date.parse(reviewAt) <= Date.parse(performedAt)) {
		throw new Error(`${prefix}.reviewAt must be after performedAt`);
	}
	if (
		expiresAt !== undefined &&
		Date.parse(expiresAt) <= Date.parse(performedAt)
	) {
		throw new Error(`${prefix}.expiresAt must be after performedAt`);
	}
	return {
		id,
		capabilityId,
		environment: requiredString(value.environment, `${prefix}.environment`),
		scope: requiredString(value.scope, `${prefix}.scope`),
		owner: requiredString(value.owner, `${prefix}.owner`),
		verifier: requiredString(value.verifier, `${prefix}.verifier`),
		source: requiredString(value.source, `${prefix}.source`),
		performedAt,
		expiresAt,
		reviewAt,
		status,
		state,
		visibility,
		publicSummary,
	};
}

function timestamp(value: unknown, field: string): string {
	const timestamp = requiredString(value, field);
	if (
		!/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z)$/.test(timestamp) ||
		!Number.isFinite(Date.parse(timestamp))
	) {
		throw new Error(`${field} must be an ISO timestamp`);
	}
	return timestamp;
}

function enumValue<const T extends readonly string[]>(
	value: unknown,
	values: T,
	field: string,
): T[number] {
	if (typeof value !== "string" || !values.includes(value as T[number])) {
		throw new Error(`${field} is not supported`);
	}
	return value as T[number];
}

function stringArray(value: unknown, field: string): string[] {
	if (
		!Array.isArray(value) ||
		value.some((entry) => typeof entry !== "string" || !entry.trim())
	) {
		throw new Error(`${field} must be an array of non-empty strings`);
	}
	if (new Set(value).size !== value.length) {
		throw new Error(`${field} contains duplicates`);
	}
	return value;
}

function requiredString(value: unknown, field: string): string {
	if (typeof value !== "string" || !value.trim()) {
		throw new Error(`${field} must be a non-empty string`);
	}
	return value;
}

function rejectUnknownFields(
	value: Record<string, unknown>,
	allowed: Set<string>,
	field: string,
) {
	const unknown = Object.keys(value).find((key) => !allowed.has(key));
	if (unknown) throw new Error(`${field} has unknown field ${unknown}`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}
