import type {
	DashboardWidget,
	FrontendServiceRequirement,
	NavItem,
	PluginNavigation,
	PluginRoute,
} from "@codefly/saas-plugin-contract";

/**
 * Schema identity for `plugin.codefly.yaml`. The api group and version pin the
 * manifest shape independently of any plugin's own version. This mirrors the
 * `apiVersion`/`kind` header obin's lodestar uses for `SolutionSpec` so the two
 * manifests can be told apart by their headers, never by guessing.
 */
export const PLUGIN_MANIFEST_API_VERSION = "plugin.codefly.dev/v1" as const;
export const PLUGIN_MANIFEST_KIND = "Plugin" as const;

/** Browser and service protocols the platform can route. */
export type ApiProtocol = "connect" | "rest" | "grpc";

/** Platform capability a plugin backend requires to run (obin's `needs`). */
export type CapabilityRequirementOptionality = "required" | "optional";

/** Typed value kinds accepted by a plugin's configuration schema. */
export type ConfigValueType = "string" | "int" | "bool" | "duration" | "url";

/** Presentation scope of a persisted schema owned by the plugin. */
export type MigrationScope = "shared" | "tenant";

/** Identity block: who the plugin is, independent of what it contributes. */
export interface PluginIdentity {
	/** Stable, globally unique logical name. Never a display string. */
	name: string;
	/** Semantic version of this manifest's plugin, not the schema. */
	version: string;
	displayName?: string;
	description?: string;
	/** Owning publisher domain, e.g. `codefly.dev`. */
	publisher?: string;
}

/** One backend service the plugin ships or owns. */
export interface PluginService {
	name: string;
	description?: string;
	/** Sorted set of endpoint protocols this service exposes. */
	endpoints: readonly ApiProtocol[];
}

/** A stable API contract this plugin provides to other solutions. */
export interface ApiExpose {
	contract: string;
	/** Positive major implemented behind this contract. */
	major: number;
	protocols: readonly ApiProtocol[];
}

/** A stable API contract this plugin depends on from another solution. */
export interface ApiConsume {
	contract: string;
	/** Exact positive major this plugin is built against. */
	major: number;
	/** Plugin-local logical name for the dependency; never a deployment address. */
	alias?: string;
}

export interface PluginApi {
	exposes?: readonly ApiExpose[];
	consumes?: readonly ApiConsume[];
}

/** A domain event this plugin emits. */
export interface EventPublication {
	/** Namespaced, versioned event type, e.g. `guardrail.triggered.v1`. */
	type: string;
	description?: string;
}

/** A domain event this plugin consumes with a plugin-owned handler id. */
export interface EventSubscription {
	type: string;
	handler: string;
	description?: string;
}

export interface PluginEvents {
	publishes?: readonly EventPublication[];
	subscribes?: readonly EventSubscription[];
}

/**
 * Frontend contribution block. The plugin's identity supplies the contract
 * version and name, so the manifest never repeats them here; the fields that
 * remain are exactly the presentation facts owned by the frontend contract.
 */
export interface PluginUi {
	navigation?: PluginNavigation;
	navItems?: readonly NavItem[];
	routes?: readonly PluginRoute[];
	widgets?: readonly DashboardWidget[];
	/** Browser-facing backend requirements resolved by the host's plugin BFF. */
	services?: readonly FrontendServiceRequirement[];
}

/** A permission this plugin defines and enforces on the backend. */
export interface PluginPermission {
	/** `resource:action` identifier, e.g. `guardrail:read`. */
	id: string;
	description?: string;
}

/** An entitlement gating a plugin feature behind a plan or grant. */
export interface PluginEntitlement {
	id: string;
	description?: string;
	/** Whether every tenant holds this entitlement absent an explicit grant. */
	defaultGranted?: boolean;
}

/** One typed configuration key the plugin reads at runtime. */
export interface PluginConfigKey {
	/** Environment-style key, e.g. `GUARDRAILS_THRESHOLD`. */
	key: string;
	type: ConfigValueType;
	required?: boolean;
	/** Marks a value that must be delivered as a secret, never in plain config. */
	secret?: boolean;
	description?: string;
}

/** One database migration the plugin owns. */
export interface PluginMigration {
	/** Ordered, unique migration id, e.g. `0001_init`. */
	id: string;
	scope: MigrationScope;
	description?: string;
}

/** One allowed outbound network destination. */
export interface PluginEgress {
	/** Hostname or single-label wildcard, e.g. `api.example.com` or `*.example.com`. */
	host: string;
	/** Sorted set of allowed destination ports; defaults to 443 when omitted. */
	ports?: readonly number[];
	description?: string;
}

/** A platform capability the plugin requires to run. */
export interface CapabilityRequirement {
	/** Namespaced capability id, e.g. `store:postgres` or `secrets:vault`. */
	capability: string;
	optionality?: CapabilityRequirementOptionality;
}

/** One declarative lifecycle step naming a plugin-owned job. */
export interface LifecycleStep {
	job: string;
	description?: string;
}

/** Install, upgrade, and uninstall reconciliation for the plugin. */
export interface PluginLifecycle {
	install?: readonly LifecycleStep[];
	upgrade?: readonly LifecycleStep[];
	uninstall?: readonly LifecycleStep[];
}

/** Detached signature over the manifest and its artifacts. */
export interface ManifestSignature {
	algorithm: string;
	keyId: string;
	value: string;
}

/** A pinned artifact hash the runtime verifies before install. */
export interface ManifestArtifact {
	path: string;
	sha256: string;
}

export interface PluginIntegrity {
	signature?: ManifestSignature;
	artifacts?: readonly ManifestArtifact[];
}

/**
 * The unified `plugin.codefly.yaml` manifest: one file spanning a plugin's
 * backend services, API compatibility, events, frontend contributions,
 * required platform capabilities, permissions, entitlements, configuration,
 * migrations, egress, lifecycle, and integrity.
 *
 * The shared identity/services/api/events/ui/needs/permissions/lifecycle facts
 * project losslessly onto obin's `SolutionSpec`; the starter-only sections
 * carry through a documented namespace. See `toSolutionSpec`.
 */
export interface PluginManifest {
	apiVersion: typeof PLUGIN_MANIFEST_API_VERSION;
	kind: typeof PLUGIN_MANIFEST_KIND;
	metadata: PluginIdentity;
	services?: readonly PluginService[];
	api?: PluginApi;
	events?: PluginEvents;
	ui?: PluginUi;
	needs?: readonly CapabilityRequirement[];
	permissions?: readonly PluginPermission[];
	entitlements?: readonly PluginEntitlement[];
	config?: readonly PluginConfigKey[];
	migrations?: readonly PluginMigration[];
	egress?: readonly PluginEgress[];
	lifecycle?: PluginLifecycle;
	integrity?: PluginIntegrity;
}
