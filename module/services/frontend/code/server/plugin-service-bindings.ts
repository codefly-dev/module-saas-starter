import {
	type FrontendServiceAllowlist,
	type FrontendServiceAllowlistEntry,
	isServiceableFrontendService,
} from "@codefly-dev/saas-plugin-contract";
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
	const entry = Array.isArray(allowlist?.entries)
		? allowlist.entries.find(
				(candidate) => candidate.plugin === plugin && candidate.alias === alias,
			)
		: undefined;
	if (!entry) return { ok: false, reason: "not_installed" };
	// The composition's own rules, re-checked against the GENERATED projection
	// this server actually reads (issue #1121). The allowlist is a build
	// artifact, so a build-time rule nothing re-checks is one a stale or
	// hand-edited file walks straight past: a `rest` entry whose prefix is `""`
	// is not the root the old check compared against, yet it lands every
	// relative path on the upstream origin — `admin/secret` reaches
	// `https://upstream/admin/secret` and is answered under the viewer's own
	// bearer. An entry that cannot be served is `unavailable`, never
	// `not_installed`: the alias IS installed, and this host cannot serve it.
	if (!isServiceableFrontendService(entry)) {
		return { ok: false, reason: "unavailable" };
	}

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
