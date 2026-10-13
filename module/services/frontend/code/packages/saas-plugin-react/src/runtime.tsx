"use client";

import {
	encodeFrontendServicePath,
	type InstalledFrontendService,
	isFrontendServicePath,
	resolveConnectProcedure,
} from "@codefly-dev/saas-plugin-contract";
import {
	FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH,
	type GetFrontendPluginCapabilitiesResponse,
	parseFrontendPluginCapabilities,
} from "@codefly-dev/saas-plugin-contract/capabilities";
import { createContext, type ReactNode, useContext, useMemo } from "react";
import {
	PluginAvailabilityError,
	pluginErrorFromResponse,
} from "./availability.js";

export type {
	PluginAvailabilityErrorOptions,
	PluginAvailabilityState,
	PluginFailure,
	PluginFailureState,
} from "./availability.js";
export {
	PluginAvailabilityError,
	pluginErrorFromResponse,
	toPluginFailure,
} from "./availability.js";

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;

export type PluginTransportConfigurationErrorCode =
	| "forbidden_header"
	| "invalid_alias"
	| "invalid_path"
	| "invalid_plugin";

export class PluginTransportConfigurationError extends Error {
	readonly code: PluginTransportConfigurationErrorCode;

	constructor(code: PluginTransportConfigurationErrorCode, message: string) {
		super(message);
		this.name = "PluginTransportConfigurationError";
		this.code = code;
	}
}

export type PluginServicePath = string | readonly string[];
export type PluginServiceQuery =
	| URLSearchParams
	| Readonly<Record<string, boolean | number | string | undefined>>;
export type PluginServiceRequestInit = Omit<
	RequestInit,
	"cache" | "credentials" | "redirect"
> & {
	query?: PluginServiceQuery;
};

export interface PluginServiceTransport {
	readonly plugin: string;
	readonly alias: string;
	capabilities(): Promise<PluginServiceCapabilities>;
	request(
		path: PluginServicePath,
		init?: PluginServiceRequestInit,
	): Promise<Response>;
}

export type PluginServiceCapabilities = GetFrontendPluginCapabilitiesResponse;

export interface PluginRuntime {
	service(plugin: string, alias: string): PluginServiceTransport;
}

export interface PluginRuntimeOptions {
	/** Host-owned session accessor. It remains closure-private to product code. */
	getAccessToken(): string | null;
	fetch?: typeof fetch;
	/**
	 * The installed service requirements, so the client can apply the host's
	 * own admission rule before a request leaves the browser. It carries no
	 * address, credential or upstream origin — only what the plugin declared.
	 * Omitting it leaves the host the sole authority, as before.
	 */
	services?: readonly InstalledFrontendService[];
}

function assertLogicalID(value: string, kind: "alias" | "plugin"): void {
	if (!LOGICAL_ID.test(value)) {
		throw new PluginTransportConfigurationError(
			kind === "plugin" ? "invalid_plugin" : "invalid_alias",
			`Invalid plugin service ${kind} '${value}'`,
		);
	}
}

/** The reserved capability probe, which the host translates rather than forwards. */
function isReservedCapabilityPath(segments: readonly string[]): boolean {
	return (
		segments.length === FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH.length &&
		segments.every(
			(segment, index) =>
				segment === FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH[index],
		)
	);
}

function pathSegments(
	path: PluginServicePath,
	requirement: InstalledFrontendService | undefined,
): string[] {
	const segments = typeof path === "string" ? path.split("/") : [...path];
	// The rule is the host's, read from the contract package rather than
	// restated: safe relative segments, the last of which may carry a custom
	// verb (`records:recover`). A client that refused one the host forwards would
	// put a declared binding out of reach with no server ever answering.
	if (!isFrontendServicePath(segments)) {
		throw new PluginTransportConfigurationError(
			"invalid_path",
			"Plugin service paths must contain only safe relative segments, with an optional custom verb on the last one",
		);
	}
	// The host's Connect rule, applied here too when the runtime was given the
	// installed requirements. The BFF refuses a procedure outside the declared
	// services either way; running the same rule in the browser means the two
	// halves cannot disagree about a shape, which is the whole reason the rule
	// lives in one package. Without the requirements this is the generic path
	// check alone, and the host stays the authority — a client that guessed
	// would refuse routes a composition had legitimately installed. The
	// reserved capability path is exempt: it is the contract's own handshake,
	// translated by the host rather than sent as a procedure.
	if (
		requirement?.protocol === "connect" &&
		!isReservedCapabilityPath(segments)
	) {
		if (!resolveConnectProcedure(requirement.connectServices ?? [], segments)) {
			throw new PluginTransportConfigurationError(
				"invalid_path",
				`Plugin service '${requirement.plugin}/${requirement.alias}' speaks Connect: the path must name one procedure of a declared service`,
			);
		}
	}
	return segments;
}

function queryString(query: PluginServiceQuery | undefined): string {
	if (!query) return "";
	if (query instanceof URLSearchParams) {
		const params = new URLSearchParams(query);
		params.sort();
		const value = params.toString();
		return value ? `?${value}` : "";
	}
	const params = new URLSearchParams();
	for (const key of Object.keys(query).sort()) {
		const value = query[key];
		if (value !== undefined) params.set(key, String(value));
	}
	const value = params.toString();
	return value ? `?${value}` : "";
}

function forbiddenHeader(name: string): boolean {
	const normalized = name.toLowerCase();
	return (
		normalized === "authorization" ||
		normalized === "cookie" ||
		normalized === "host" ||
		normalized === "origin" ||
		normalized === "proxy-authorization" ||
		normalized.startsWith("forwarded") ||
		normalized.startsWith("x-")
	);
}

function productHeaders(input: HeadersInit | undefined): Headers {
	const headers = new Headers(input);
	for (const name of headers.keys()) {
		if (forbiddenHeader(name)) {
			throw new PluginTransportConfigurationError(
				"forbidden_header",
				`Plugin service requests cannot set '${name}'`,
			);
		}
	}
	return headers;
}

function createServiceTransport(
	runtimeFetch: typeof fetch,
	getAccessToken: () => string | null,
	plugin: string,
	alias: string,
	requirement: InstalledFrontendService | undefined,
): PluginServiceTransport {
	assertLogicalID(plugin, "plugin");
	assertLogicalID(alias, "alias");
	const basePath = `/api/plugins/${encodeURIComponent(plugin)}/${encodeURIComponent(alias)}`;
	let capabilityRequest: Promise<PluginServiceCapabilities> | undefined;

	async function request(
		path: PluginServicePath,
		init: PluginServiceRequestInit = {},
	): Promise<Response> {
		const { query, ...requestInit } = init;
		const segments = pathSegments(path, requirement);
		const headers = productHeaders(requestInit.headers);
		const token = getAccessToken();
		if (token) headers.set("authorization", `Bearer ${token}`);

		return runtimeFetch(
			`${basePath}/${encodeFrontendServicePath(segments)}${queryString(query)}`,
			{
				...requestInit,
				headers,
				cache: "no-store",
				credentials: "omit",
				mode: "same-origin",
				redirect: "error",
			},
		);
	}

	function capabilities(): Promise<PluginServiceCapabilities> {
		if (capabilityRequest) return capabilityRequest;
		const attempt = (async () => {
			const response = await request(FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH, {
				headers: { accept: "application/json" },
			});
			if (!response.ok) throw await pluginErrorFromResponse(response);
			try {
				return parseFrontendPluginCapabilities(await response.json());
			} catch (cause) {
				throw new PluginAvailabilityError("incompatible", {
					code: "backend_incompatible",
					requestId: response.headers.get("x-request-id") ?? undefined,
					cause,
				});
			}
		})();
		const cachedAttempt = attempt.catch((error: unknown) => {
			if (capabilityRequest === cachedAttempt) capabilityRequest = undefined;
			throw error;
		});
		capabilityRequest = cachedAttempt;
		return cachedAttempt;
	}

	return Object.freeze({
		plugin,
		alias,
		capabilities,
		request,
	});
}

/** Create the closure-backed runtime injected by the host application. */
export function createPluginRuntime(
	options: PluginRuntimeOptions,
): PluginRuntime {
	if (typeof options.getAccessToken !== "function") {
		throw new TypeError("createPluginRuntime requires getAccessToken");
	}
	const runtimeFetch: typeof fetch =
		options.fetch ?? ((input, init) => globalThis.fetch(input, init));
	const requirements = new Map<string, InstalledFrontendService>(
		(options.services ?? []).map((service) => [
			`${service.plugin}/${service.alias}`,
			service,
		]),
	);
	const services = new Map<string, PluginServiceTransport>();

	return Object.freeze({
		service(plugin: string, alias: string) {
			const key = `${plugin}/${alias}`;
			const existing = services.get(key);
			if (existing) return existing;
			const transport = createServiceTransport(
				runtimeFetch,
				options.getAccessToken,
				plugin,
				alias,
				requirements.get(key),
			);
			services.set(key, transport);
			return transport;
		},
	});
}

const PluginRuntimeContext = createContext<PluginRuntime | null>(null);

export interface PluginRuntimeProviderProps {
	runtime: PluginRuntime;
	children: ReactNode;
}

export function PluginRuntimeProvider({
	runtime,
	children,
}: PluginRuntimeProviderProps) {
	return (
		<PluginRuntimeContext.Provider value={runtime}>
			{children}
		</PluginRuntimeContext.Provider>
	);
}

export function usePluginRuntime(): PluginRuntime {
	const runtime = useContext(PluginRuntimeContext);
	if (!runtime) {
		throw new Error(
			"usePluginRuntime must be used within PluginRuntimeProvider",
		);
	}
	return runtime;
}

export function usePluginService(
	plugin: string,
	alias: string,
): PluginServiceTransport {
	const runtime = usePluginRuntime();
	return useMemo(
		() => runtime.service(plugin, alias),
		[runtime, plugin, alias],
	);
}
