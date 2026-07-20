"use client";

import {
	FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH,
	parseFrontendPluginCapabilities,
	type GetFrontendPluginCapabilitiesResponse,
} from "@codefly/saas-plugin-contract/capabilities";
import { createContext, type ReactNode, useContext, useMemo } from "react";
import {
	pluginErrorFromResponse,
	PluginAvailabilityError,
} from "./availability.js";

export type {
	PluginAvailabilityErrorOptions,
	PluginAvailabilityState,
	PluginFailure,
	PluginFailureState,
} from "./availability.js";
export {
	pluginErrorFromResponse,
	PluginAvailabilityError,
	toPluginFailure,
} from "./availability.js";

const LOGICAL_ID = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const SAFE_PATH_SEGMENT = /^[A-Za-z0-9._~-]+$/;

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
}

function assertLogicalID(value: string, kind: "alias" | "plugin"): void {
	if (!LOGICAL_ID.test(value)) {
		throw new PluginTransportConfigurationError(
			kind === "plugin" ? "invalid_plugin" : "invalid_alias",
			`Invalid plugin service ${kind} '${value}'`,
		);
	}
}

function pathSegments(path: PluginServicePath): string[] {
	const segments = typeof path === "string" ? path.split("/") : [...path];
	if (
		segments.length === 0 ||
		segments.some(
			(segment) =>
				segment === "." ||
				segment === ".." ||
				segment.length === 0 ||
				segment.length > 256 ||
				!SAFE_PATH_SEGMENT.test(segment),
		)
	) {
		throw new PluginTransportConfigurationError(
			"invalid_path",
			"Plugin service paths must contain only safe relative segments",
		);
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
		const segments = pathSegments(path);
		const headers = productHeaders(requestInit.headers);
		const token = getAccessToken();
		if (token) headers.set("authorization", `Bearer ${token}`);

		return runtimeFetch(
			`${basePath}/${segments.map(encodeURIComponent).join("/")}${queryString(query)}`,
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
