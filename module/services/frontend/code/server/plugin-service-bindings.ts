import type {
	FrontendServiceAllowlist,
	FrontendServiceAllowlistEntry,
} from "@codefly/saas-plugin-contract";
import { getEndpoints, type ServiceEndpoint } from "codefly";

import generatedAllowlist from "./plugin-service-allowlist.generated.json";

export type CodeflyServiceEndpoint = Pick<
	ServiceEndpoint,
	"module" | "service" | "name" | "protocol" | "address"
>;

export interface ResolvedPluginService {
	entry: FrontendServiceAllowlistEntry;
	baseURL: string;
}

export type PluginServiceResolution =
	| { ok: true; value: ResolvedPluginService }
	| { ok: false; reason: "not_installed" | "unavailable" };

/** Read only endpoints exposed by Codefly's SDK for declared dependencies. */
export function codeflyServiceEndpoints(
	resolve: () => readonly ServiceEndpoint[] = getEndpoints,
): CodeflyServiceEndpoint[] {
	return resolve().map(({ module, service, name, protocol, address }) => ({
		module,
		service,
		name,
		protocol,
		address,
	}));
}

function normalizeServerEndpoint(address: string): string | null {
	let parsed: URL;
	try {
		parsed = new URL(address);
	} catch {
		return null;
	}
	if (
		!(["http:", "https:"] as const).includes(
			parsed.protocol as "http:" | "https:",
		)
	) {
		return null;
	}
	if (
		parsed.username ||
		parsed.password ||
		parsed.search ||
		parsed.hash ||
		(parsed.pathname !== "" && parsed.pathname !== "/")
	) {
		return null;
	}
	return parsed.origin;
}

/**
 * Resolve one generated logical target against Codefly's server environment.
 * Concrete addresses never enter the application config or browser bundle.
 */
export function resolvePluginServiceFrom(
	allowlist: FrontendServiceAllowlist,
	endpoints: readonly CodeflyServiceEndpoint[],
	plugin: string,
	alias: string,
): PluginServiceResolution {
	const entry = allowlist.entries.find(
		(candidate) => candidate.plugin === plugin && candidate.alias === alias,
	);
	if (!entry) return { ok: false, reason: "not_installed" };

	const matches = endpoints.filter(
		(endpoint) =>
			endpoint.module === entry.target.module &&
			endpoint.service === entry.target.service &&
			endpoint.name === entry.target.endpoint &&
			endpoint.protocol.toLowerCase() === entry.protocol,
	);
	if (matches.length !== 1) return { ok: false, reason: "unavailable" };

	const baseURL = normalizeServerEndpoint(matches[0].address);
	if (!baseURL) return { ok: false, reason: "unavailable" };
	return { ok: true, value: { entry, baseURL } };
}

export function resolvePluginService(
	plugin: string,
	alias: string,
): PluginServiceResolution {
	return resolvePluginServiceFrom(
		generatedAllowlist as FrontendServiceAllowlist,
		codeflyServiceEndpoints(),
		plugin,
		alias,
	);
}
