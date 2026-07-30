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
	const matches = runtime
		.endpoints()
		.filter(
			(endpoint) =>
				endpoint.module === currentModule &&
				endpoint.service === currentService &&
				endpoint.name === "http" &&
				endpoint.protocol === "HTTP",
		);
	if (matches.length > 1) return undefined;

	// A Codefly runtime identity must have exactly one SDK-discovered own HTTP
	// endpoint. Outside Codefly (isolated component tests), the request origin
	// is the only available public origin.
	const publicOrigin =
		currentModule || currentService
			? matches.length === 1
				? canonicalOrigin(matches[0].address)
				: undefined
			: canonicalOrigin(requestOrigin);
	if (!publicOrigin) return undefined;

	return { internalToken, publicOrigin };
}
