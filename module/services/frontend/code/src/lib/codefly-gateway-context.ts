import {
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	getWorkspaceSecret,
	type ServiceEndpoint,
} from "codefly";

export interface CodeflyGatewayContext {
	internalToken: string;
	publicOrigin: string;
}

export interface CodeflyRuntimeReader {
	currentModule(): string;
	currentService(): string;
	endpoints(): ServiceEndpoint[];
	workspaceSecret(name: string, key: string): string | undefined;
}

const runtimeSDK: CodeflyRuntimeReader = {
	currentModule: getCurrentModule,
	currentService: getCurrentService,
	endpoints: getEndpoints,
	workspaceSecret: getWorkspaceSecret,
};

function canonicalOrigin(candidate: string): string | undefined {
	try {
		const parsed = new URL(candidate);
		if (
			!["http:", "https:"].includes(parsed.protocol) ||
			parsed.username ||
			parsed.password ||
			(parsed.pathname !== "" && parsed.pathname !== "/") ||
			parsed.search ||
			parsed.hash
		) {
			return undefined;
		}
		return parsed.origin;
	} catch {
		return undefined;
	}
}

function isLoopbackOrigin(origin: string): boolean {
	const host = new URL(origin).hostname.toLowerCase().replace(/^\[|\]$/g, "");
	if (host === "localhost" || host === "::1") return true;
	const octets = host.split(".");
	return octets.length === 4 && octets[0] === "127";
}

/**
 * Resolve the server-side credentials used between the public frontend and the
 * private auth gateway. Codefly's injected representation remains entirely
 * behind sdk-js; this library only consumes typed SDK values.
 */
export function resolveCodeflyGatewayContext(
	requestOrigin: string,
	runtime: CodeflyRuntimeReader = runtimeSDK,
): CodeflyGatewayContext | undefined {
	const internalToken = runtime
		.workspaceSecret("internal-auth", "CODEFLY_INTERNAL_TOKEN")
		?.trim();
	if (!internalToken) return undefined;

	const currentModule = runtime.currentModule().trim();
	const currentService = runtime.currentService().trim();

	// Outside a Codefly runtime (isolated component tests) there is no own
	// endpoint to discover, so the request origin is the only public origin.
	if (!currentModule && !currentService) {
		const publicOrigin = canonicalOrigin(requestOrigin);
		if (!publicOrigin) return undefined;
		return { internalToken, publicOrigin };
	}

	const matches = runtime
		.endpoints()
		.filter(
			(endpoint) =>
				endpoint.module === currentModule &&
				endpoint.service === currentService &&
				endpoint.name === "http" &&
				endpoint.protocol === "HTTP",
		);
	// A Codefly runtime identity must have exactly one SDK-discovered own HTTP
	// endpoint.
	if (matches.length !== 1) return undefined;
	const endpointOrigin = canonicalOrigin(matches[0].address);
	if (!endpointOrigin) return undefined;

	// The render bakes the own HTTP endpoint as a loopback placeholder
	// (localhost:8080) that carries no real ingress host: the true public origin
	// is a per-cell/per-ingress fact the SDK cannot know. Fall back to the actual
	// browser request in that case. A genuine local-dev endpoint is loopback too,
	// and its request origin is the same host, so this stays correct in dev. Once
	// the render carries a real non-loopback ingress host, that host keeps
	// priority over the caller-influenced request origin.
	const publicOrigin = isLoopbackOrigin(endpointOrigin)
		? canonicalOrigin(requestOrigin)
		: endpointOrigin;
	if (!publicOrigin) return undefined;

	return { internalToken, publicOrigin };
}
