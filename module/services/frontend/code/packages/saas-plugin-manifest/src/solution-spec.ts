import type {
	ApiConsume,
	ApiExpose,
	CapabilityRequirement,
	PluginEvents,
	PluginManifest,
	PluginPermission,
	PluginService,
	PluginUi,
} from "./contracts.js";

/**
 * The subset of obin's lodestar `SolutionSpec` that the unified plugin manifest
 * shares (lodestar `docs/design/solution-anatomy.md`, obin-ai/lodestar#13, #15):
 * identity, services, api exposes/consumes, events, ui extensions, needs,
 * permissions, and lifecycle. This declaration is the contract the projection
 * targets; it is intentionally the shared shape only, never a second copy of
 * the starter's manifest.
 */
export const SOLUTION_SPEC_API_VERSION = "solution.obin.dev/v1" as const;

export interface SolutionIdentity {
	name: string;
	version: string;
	displayName?: string;
	description?: string;
	publisher?: string;
}

export interface SolutionLifecycleStep {
	job: string;
	description?: string;
}

export interface SolutionLifecycle {
	install?: readonly SolutionLifecycleStep[];
	upgrade?: readonly SolutionLifecycleStep[];
	uninstall?: readonly SolutionLifecycleStep[];
}

/**
 * Starter-only manifest sections that lodestar's `SolutionSpec` does not model
 * one-to-one. They ride through the projection under a documented namespace so
 * the mapping is lossless and obin can adopt them deliberately rather than by
 * accident. See `plugin-manifest-schema.md`.
 */
export interface SolutionCodeflyExtensions {
	entitlements?: PluginManifest["entitlements"];
	config?: PluginManifest["config"];
	migrations?: PluginManifest["migrations"];
	egress?: PluginManifest["egress"];
	integrity?: PluginManifest["integrity"];
}

export interface SolutionSpec {
	apiVersion: typeof SOLUTION_SPEC_API_VERSION;
	kind: "Solution";
	metadata: SolutionIdentity;
	services?: readonly PluginService[];
	api?: {
		exposes?: readonly ApiExpose[];
		consumes?: readonly ApiConsume[];
	};
	events?: PluginEvents;
	ui?: PluginUi;
	needs?: readonly CapabilityRequirement[];
	permissions?: readonly PluginPermission[];
	lifecycle?: SolutionLifecycle;
	extensions?: { "x-codefly": SolutionCodeflyExtensions };
}

function nonEmpty<T>(value: readonly T[] | undefined): value is readonly T[] {
	return value !== undefined && value.length > 0;
}

/**
 * Projects a unified plugin manifest onto obin's `SolutionSpec`. Shared facts
 * map directly; starter-only sections carry through `extensions['x-codefly']`.
 * The projection is total and side-effect free — it is the single point where
 * the two manifests meet, so they can converge without forking either schema.
 */
export function toSolutionSpec(manifest: PluginManifest): SolutionSpec {
	const extensions: SolutionCodeflyExtensions = {};
	if (nonEmpty(manifest.entitlements))
		extensions.entitlements = manifest.entitlements;
	if (nonEmpty(manifest.config)) extensions.config = manifest.config;
	if (nonEmpty(manifest.migrations))
		extensions.migrations = manifest.migrations;
	if (nonEmpty(manifest.egress)) extensions.egress = manifest.egress;
	if (manifest.integrity !== undefined)
		extensions.integrity = manifest.integrity;

	const spec: SolutionSpec = {
		apiVersion: SOLUTION_SPEC_API_VERSION,
		kind: "Solution",
		metadata: manifest.metadata,
	};
	if (nonEmpty(manifest.services)) spec.services = manifest.services;
	if (
		manifest.api?.exposes !== undefined ||
		manifest.api?.consumes !== undefined
	)
		spec.api = manifest.api;
	if (manifest.events !== undefined) spec.events = manifest.events;
	if (manifest.ui !== undefined) spec.ui = manifest.ui;
	if (nonEmpty(manifest.needs)) spec.needs = manifest.needs;
	if (nonEmpty(manifest.permissions)) spec.permissions = manifest.permissions;
	if (manifest.lifecycle !== undefined) spec.lifecycle = manifest.lifecycle;
	if (Object.keys(extensions).length > 0)
		spec.extensions = { "x-codefly": extensions };

	return spec;
}
