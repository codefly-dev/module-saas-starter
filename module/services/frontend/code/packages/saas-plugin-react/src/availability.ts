export type PluginAvailabilityState =
	| "loading"
	| "ready"
	| "unavailable"
	| "incompatible"
	| "failed";

export type PluginFailureState = Exclude<
	PluginAvailabilityState,
	"loading" | "ready"
>;

export interface PluginFailure {
	state: PluginFailureState;
	code: string;
	requestId?: string;
	retryAfterSeconds?: number;
}

export interface PluginAvailabilityErrorOptions {
	code: string;
	requestId?: string;
	retryAfterSeconds?: number;
	cause?: unknown;
}

const SAFE_CODE = /^[a-z][a-z0-9_]{0,63}$/;
const SAFE_REQUEST_ID = /^[A-Za-z0-9._:-]{1,128}$/;
const MAX_RETRY_AFTER_SECONDS = 86_400;

function safeCode(code: unknown, fallback: string): string {
	return typeof code === "string" && SAFE_CODE.test(code) ? code : fallback;
}

function safeRequestID(requestId: unknown): string | undefined {
	return typeof requestId === "string" && SAFE_REQUEST_ID.test(requestId)
		? requestId
		: undefined;
}

function safeRetryAfter(value: unknown): number | undefined {
	if (typeof value !== "number") return undefined;
	return Number.isSafeInteger(value) &&
		value >= 0 &&
		value <= MAX_RETRY_AFTER_SECONDS
		? value
		: undefined;
}

function defaultMessage(state: PluginFailureState): string {
	switch (state) {
		case "unavailable":
			return "Plugin service is temporarily unavailable";
		case "incompatible":
			return "Plugin is incompatible with this application";
		case "failed":
			return "Plugin contribution failed";
	}
}

export class PluginAvailabilityError extends Error {
	readonly failure: PluginFailure;

	constructor(
		state: PluginFailureState,
		options: PluginAvailabilityErrorOptions,
	) {
		const code = safeCode(options.code, "plugin_failed");
		const requestId = safeRequestID(options.requestId);
		const retryAfterSeconds = safeRetryAfter(options.retryAfterSeconds);
		super(defaultMessage(state), { cause: options.cause });
		this.name = "PluginAvailabilityError";
		this.failure = Object.freeze({
			state,
			code,
			...(requestId ? { requestId } : {}),
			...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {}),
		});
	}
}

export function toPluginFailure(error: unknown): PluginFailure {
	return error instanceof PluginAvailabilityError
		? error.failure
		: Object.freeze({ state: "failed", code: "render_failed" });
}

function failureState(code: string): PluginFailureState {
	if (code === "backend_unavailable") return "unavailable";
	if (code === "backend_incompatible" || code === "plugin_service_not_found")
		return "incompatible";
	return "failed";
}

function retryAfterSeconds(response: Response): number | undefined {
	const value = response.headers.get("retry-after");
	if (!value || !/^\d+$/.test(value)) return undefined;
	return safeRetryAfter(Number(value));
}

/** Convert a failed plugin response into a safe, renderable state. */
export async function pluginErrorFromResponse(
	response: Response,
): Promise<PluginAvailabilityError> {
	if (response.ok)
		throw new TypeError("pluginErrorFromResponse requires a failed response");

	let body: { code?: unknown } = {};
	if (
		response.headers
			.get("content-type")
			?.toLowerCase()
			.startsWith("application/problem+json")
	) {
		try {
			body = (await response.clone().json()) as typeof body;
		} catch {
			body = {};
		}
	}
	const code = safeCode(body.code, `http_${response.status}`);
	return new PluginAvailabilityError(failureState(code), {
		code,
		// Only the host BFF response header is trusted correlation metadata.
		// Product/upstream problem bodies may contain their own identifiers, but
		// those are not safe to present as a host request ID.
		requestId: safeRequestID(response.headers.get("x-request-id")),
		retryAfterSeconds: retryAfterSeconds(response),
	});
}
