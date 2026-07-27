import { toJsonString } from "@bufbuild/protobuf";
import type { FrontendServiceProtocol } from "@codefly/saas-plugin-contract";
import {
	emptyFrontendPluginCapabilitiesRequest,
	FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH,
	FRONTEND_PLUGIN_CAPABILITIES_CONNECT_PROCEDURE,
	FRONTEND_PLUGIN_CAPABILITIES_REST_PATH,
	frontendPluginCapabilitiesToJson,
	GetFrontendPluginCapabilitiesRequestSchema,
	parseFrontendPluginCapabilities,
	supportsFrontendPluginContract,
} from "@codefly/saas-plugin-contract/capabilities";

import {
	type PluginServiceResolution,
	resolvePluginService,
} from "./plugin-service-bindings";

const DEFAULT_REQUEST_LIMIT = 1024 * 1024;
const DEFAULT_RESPONSE_LIMIT = 5 * 1024 * 1024;
const CAPABILITY_RESPONSE_LIMIT = 16 * 1024;
const DEFAULT_TIMEOUT_MS = 10_000;
const SAFE_PATH_SEGMENT = /^[A-Za-z0-9._~-]+$/;
const BEARER = /^Bearer [A-Za-z0-9._~-]{16,8192}$/;
const TRACEPARENT = /^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/;
const REST_METHODS = new Set(["GET", "POST", "PUT", "PATCH", "DELETE"]);
const CONNECT_METHODS = new Set(["POST"]);
const REQUEST_HEADERS = [
	"accept",
	"accept-language",
	"content-type",
	"if-match",
	"if-none-match",
	"connect-protocol-version",
	"connect-timeout-ms",
] as const;
const RESPONSE_HEADERS = [
	"content-type",
	"content-language",
	"etag",
	"last-modified",
	"retry-after",
	"vary",
] as const;

export interface PluginBffParams {
	plugin: string;
	alias: string;
	path: readonly string[];
}

export interface PluginBffDependencies {
	resolve: (plugin: string, alias: string) => PluginServiceResolution;
	fetch: typeof fetch;
	requestID: () => string;
	requestLimit: number;
	responseLimit: number;
	timeoutMs: number;
}

const defaultDependencies: PluginBffDependencies = {
	resolve: resolvePluginService,
	fetch: globalThis.fetch,
	requestID: () => crypto.randomUUID(),
	requestLimit: DEFAULT_REQUEST_LIMIT,
	responseLimit: DEFAULT_RESPONSE_LIMIT,
	timeoutMs: DEFAULT_TIMEOUT_MS,
};

const problemTitles = {
	authentication_required: "Authentication required",
	backend_incompatible: "Plugin backend incompatible",
	backend_unavailable: "Plugin backend unavailable",
	client_closed_request: "Client closed request",
	cross_origin_request: "Cross-origin request rejected",
	invalid_body: "Invalid request body",
	invalid_path: "Invalid plugin path",
	method_not_allowed: "Method not allowed",
	plugin_service_not_found: "Plugin service not found",
	request_too_large: "Request body too large",
	unsupported_media_type: "Unsupported media type",
	upstream_failed: "Plugin backend request failed",
	upstream_redirect: "Plugin backend redirect rejected",
	upstream_response_too_large: "Plugin backend response too large",
	upstream_timeout: "Plugin backend timed out",
} as const;

type ProblemCode = keyof typeof problemTitles;

function problem(
	code: ProblemCode,
	status: number,
	requestID: string,
	extra?: HeadersInit,
) {
	return Response.json(
		{
			type: `urn:codefly:problem:frontend-plugin-bff:${code}`,
			title: problemTitles[code],
			status,
			code,
			requestId: requestID,
		},
		{
			status,
			headers: {
				"cache-control": "no-store",
				"content-type": "application/problem+json",
				"x-content-type-options": "nosniff",
				"x-request-id": requestID,
				...Object.fromEntries(new Headers(extra)),
			},
		},
	);
}

function allowedMethods(protocol: FrontendServiceProtocol): Set<string> {
	return protocol === "connect" ? CONNECT_METHODS : REST_METHODS;
}

function safePath(request: Request, segments: readonly string[]): boolean {
	const rawPath = new URL(request.url).pathname;
	if (/%(?:00|25|2f|5c)/i.test(rawPath)) return false;
	return segments.every(
		(segment) =>
			segment !== "." &&
			segment !== ".." &&
			segment.length <= 256 &&
			SAFE_PATH_SEGMENT.test(segment),
	);
}

function sameOrigin(request: Request): boolean {
	const origin = request.headers.get("origin");
	if (origin) {
		try {
			if (new URL(origin).origin !== new URL(request.url).origin) return false;
		} catch {
			return false;
		}
	}
	const fetchSite = request.headers.get("sec-fetch-site");
	return !fetchSite || fetchSite === "same-origin" || fetchSite === "none";
}

function supportedContentType(
	protocol: FrontendServiceProtocol,
	value: string,
): boolean {
	const mediaType = value.split(";", 1)[0]?.trim().toLowerCase();
	if (!mediaType) return false;
	if (
		mediaType === "application/json" ||
		/^application\/[a-z0-9.+-]+\+json$/.test(mediaType)
	) {
		return true;
	}
	return (
		protocol === "connect" &&
		["application/proto", "application/connect+proto"].includes(mediaType)
	);
}

function jsonContentType(value: string | null): boolean {
	const mediaType = value?.split(";", 1)[0]?.trim().toLowerCase();
	return Boolean(
		mediaType === "application/json" ||
			(mediaType && /^application\/[a-z0-9.+-]+\+json$/.test(mediaType)),
	);
}

class BodyLimitError extends Error {}

async function readLimitedBody(
	body: ReadableStream<Uint8Array> | null,
	declaredLength: string | null,
	limit: number,
): Promise<ArrayBuffer | undefined> {
	if (declaredLength) {
		const parsed = Number(declaredLength);
		if (!Number.isSafeInteger(parsed) || parsed < 0)
			throw new TypeError("invalid length");
		if (parsed > limit) throw new BodyLimitError();
	}
	if (!body) return undefined;

	const reader = body.getReader();
	const chunks: Uint8Array[] = [];
	let total = 0;
	while (true) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > limit) {
			await reader.cancel();
			throw new BodyLimitError();
		}
		chunks.push(value);
	}
	const combined = new Uint8Array(total);
	let offset = 0;
	for (const chunk of chunks) {
		combined.set(chunk, offset);
		offset += chunk.byteLength;
	}
	return combined.buffer;
}

function proxyRequestHeaders(request: Request, requestID: string): Headers {
	const headers = new Headers();
	for (const name of REQUEST_HEADERS) {
		const value = request.headers.get(name);
		if (value) headers.set(name, value);
	}
	headers.set("authorization", request.headers.get("authorization") ?? "");
	headers.set("x-request-id", requestID);
	const traceparent = request.headers.get("traceparent")?.toLowerCase();
	const trace = traceparent ? TRACEPARENT.exec(traceparent) : null;
	if (trace && !/^0+$/.test(trace[1]) && !/^0+$/.test(trace[2])) {
		headers.set("traceparent", traceparent ?? "");
	}
	return headers;
}

function proxyResponseHeaders(response: Response, requestID: string): Headers {
	const headers = new Headers({
		"cache-control": "no-store",
		"x-content-type-options": "nosniff",
		"x-request-id": requestID,
	});
	for (const name of RESPONSE_HEADERS) {
		const value = response.headers.get(name);
		if (value) headers.set(name, value);
	}
	return headers;
}

function targetURL(
	baseURL: string,
	routePrefix: string,
	path: readonly string[],
	search: string,
) {
	const target = new URL(baseURL);
	target.pathname = `${routePrefix}${path.length ? `/${path.map(encodeURIComponent).join("/")}` : ""}`;
	target.search = search;
	return target;
}

function isCapabilityProbe(path: readonly string[]): boolean {
	return (
		path.length === FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH.length &&
		path.every(
			(segment, index) =>
				segment === FRONTEND_PLUGIN_CAPABILITIES_BFF_PATH[index],
		)
	);
}

function capabilityTargetURL(
	baseURL: string,
	protocol: FrontendServiceProtocol,
	probePath?: string,
): URL {
	const target = new URL(baseURL);
	target.pathname =
		protocol === "connect"
			? FRONTEND_PLUGIN_CAPABILITIES_CONNECT_PROCEDURE
			: (probePath ?? FRONTEND_PLUGIN_CAPABILITIES_REST_PATH);
	return target;
}

/** Constrained unary proxy used by the generic plugin BFF route. */
export async function handlePluginBffRequest(
	request: Request,
	params: PluginBffParams,
	overrides: Partial<PluginBffDependencies> = {},
): Promise<Response> {
	const dependencies = { ...defaultDependencies, ...overrides };
	const requestID = dependencies.requestID();
	const authorization = request.headers.get("authorization");
	if (!authorization || !BEARER.test(authorization)) {
		return problem("authentication_required", 401, requestID);
	}
	if (!sameOrigin(request)) {
		return problem("cross_origin_request", 403, requestID);
	}
	if (!safePath(request, params.path)) {
		return problem("invalid_path", 400, requestID);
	}

	const resolution = dependencies.resolve(params.plugin, params.alias);
	if (!resolution.ok) {
		return resolution.reason === "not_installed"
			? problem("plugin_service_not_found", 404, requestID)
			: problem("backend_unavailable", 503, requestID);
	}

	const method = request.method.toUpperCase();
	const capabilityProbe = isCapabilityProbe(params.path);
	const methods = capabilityProbe
		? new Set(["GET"])
		: allowedMethods(resolution.value.entry.protocol);
	if (!methods.has(method)) {
		return problem("method_not_allowed", 405, requestID, {
			allow: [...methods].join(", "),
		});
	}
	if (request.headers.has("content-encoding")) {
		return problem("unsupported_media_type", 415, requestID);
	}
	if (
		request.body &&
		!supportedContentType(
			resolution.value.entry.protocol,
			request.headers.get("content-type") ?? "",
		)
	) {
		return problem("unsupported_media_type", 415, requestID);
	}

	let body: ArrayBuffer | undefined;
	try {
		body = await readLimitedBody(
			request.body,
			request.headers.get("content-length"),
			dependencies.requestLimit,
		);
	} catch (error) {
		if (request.signal.aborted) {
			return problem("client_closed_request", 499, requestID);
		}
		if (error instanceof BodyLimitError) {
			return problem("request_too_large", 413, requestID);
		}
		return problem("invalid_body", 400, requestID);
	}

	const controller = new AbortController();
	let timedOut = false;
	const abortForClient = () => controller.abort(request.signal.reason);
	if (request.signal.aborted) abortForClient();
	else request.signal.addEventListener("abort", abortForClient, { once: true });
	const timer = setTimeout(() => {
		timedOut = true;
		controller.abort(
			new DOMException("plugin backend timeout", "TimeoutError"),
		);
	}, dependencies.timeoutMs);

	try {
		const incomingURL = new URL(request.url);
		const upstreamHeaders = proxyRequestHeaders(request, requestID);
		let upstreamBody: BodyInit | undefined = body;
		let upstreamMethod = method;
		let upstreamTarget: URL;
		if (capabilityProbe) {
			upstreamTarget = capabilityTargetURL(
				resolution.value.baseURL,
				resolution.value.entry.protocol,
				resolution.value.entry.compatibility.probePath,
			);
			upstreamHeaders.set("accept", "application/json");
			if (resolution.value.entry.protocol === "connect") {
				upstreamMethod = "POST";
				upstreamHeaders.set("connect-protocol-version", "1");
				upstreamHeaders.set("content-type", "application/json");
				upstreamBody = toJsonString(
					GetFrontendPluginCapabilitiesRequestSchema,
					emptyFrontendPluginCapabilitiesRequest(),
				);
			} else {
				upstreamHeaders.delete("connect-protocol-version");
				upstreamHeaders.delete("connect-timeout-ms");
				upstreamHeaders.delete("content-type");
				upstreamBody = undefined;
			}
		} else {
			upstreamTarget = targetURL(
				resolution.value.baseURL,
				resolution.value.entry.routePrefix,
				params.path,
				incomingURL.search,
			);
		}
		const upstream = await dependencies.fetch(upstreamTarget, {
			method: upstreamMethod,
			headers: upstreamHeaders,
			body: upstreamBody,
			cache: "no-store",
			redirect: "manual",
			signal: controller.signal,
		});

		if (
			upstream.status >= 300 &&
			upstream.status <= 399 &&
			upstream.status !== 304
		) {
			await upstream.body?.cancel();
			return capabilityProbe
				? problem("backend_incompatible", 426, requestID)
				: problem("upstream_redirect", 502, requestID);
		}
		if (capabilityProbe && !upstream.ok) {
			await upstream.body?.cancel();
			if (upstream.status === 401) {
				return problem("authentication_required", 401, requestID);
			}
			return upstream.status >= 500
				? problem("backend_unavailable", 503, requestID)
				: problem("backend_incompatible", 426, requestID);
		}

		let responseBody: ArrayBuffer | undefined;
		try {
			responseBody = await readLimitedBody(
				upstream.body,
				upstream.headers.get("content-length"),
				capabilityProbe
					? Math.min(dependencies.responseLimit, CAPABILITY_RESPONSE_LIMIT)
					: dependencies.responseLimit,
			);
		} catch (error) {
			if (error instanceof BodyLimitError) {
				return capabilityProbe
					? problem("backend_incompatible", 426, requestID)
					: problem("upstream_response_too_large", 502, requestID);
			}
			throw error;
		}

		if (capabilityProbe) {
			if (!jsonContentType(upstream.headers.get("content-type")))
				return problem("backend_incompatible", 426, requestID);
			try {
				const decoded = new TextDecoder("utf-8", { fatal: true }).decode(
					responseBody,
				);
				const capabilities = parseFrontendPluginCapabilities(
					JSON.parse(decoded),
				);
				if (
					!supportsFrontendPluginContract(
						capabilities,
						resolution.value.entry.compatibility,
					)
				)
					return problem("backend_incompatible", 426, requestID);
				const headers = proxyResponseHeaders(upstream, requestID);
				headers.set("content-type", "application/json");
				return Response.json(frontendPluginCapabilitiesToJson(capabilities), {
					headers,
				});
			} catch {
				return problem("backend_incompatible", 426, requestID);
			}
		}

		const hasNoBody =
			method === "HEAD" || upstream.status === 204 || upstream.status === 304;
		return new Response(hasNoBody ? null : responseBody, {
			status: upstream.status,
			headers: proxyResponseHeaders(upstream, requestID),
		});
	} catch {
		if (timedOut) return problem("upstream_timeout", 504, requestID);
		if (request.signal.aborted)
			return problem("client_closed_request", 499, requestID);
		return problem("upstream_failed", 502, requestID);
	} finally {
		clearTimeout(timer);
		request.signal.removeEventListener("abort", abortForClient);
	}
}
