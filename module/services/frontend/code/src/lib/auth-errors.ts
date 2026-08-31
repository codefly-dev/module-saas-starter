// Auth failures reach the browser as a gRPC-gateway JSON error
// (`{"code": <number>, "message": "..."}`, where `code` is the numeric gRPC
// status) carried by an HTTP status. The backend deliberately collapses every
// credential/identity failure to a single `16 / "invalid credentials"` and
// never returns the underlying reason, so the browser cannot tell those causes
// apart. The one handle that survives is the `X-Request-Id` the accounts REST
// layer stamps on every response and logs alongside the real reason — surfacing
// it lets an operator map an opaque rejection back to the server-side cause.

export interface AuthErrorDetail {
	/** HTTP status of the failed response. */
	status: number;
	/** Canonical gRPC/Connect code (e.g. "unauthenticated"), when the body carried one. */
	code?: string;
	/** Correlation id for the failed request, when the response exposed one. */
	requestId?: string;
	/** Distributed-trace id parsed from `traceparent`/`x-trace-id`, when present. */
	traceId?: string;
}

/**
 * A sign-in failure with tenant-appropriate copy (its `message`) plus the raw
 * operator-facing correlation metadata in `detail`. The UI renders `message` to
 * the user and `operatorReference(detail)` as a quiet reference line.
 */
export class AuthError extends Error {
	readonly detail: AuthErrorDetail;

	constructor(message: string, detail: AuthErrorDetail) {
		super(message);
		this.name = "AuthError";
		this.detail = detail;
	}
}

// Numeric gRPC status codes → canonical names. Only the codes the auth boundary
// actually emits are mapped; anything else falls back to the HTTP status.
const GRPC_CODE_NAMES: Record<number, string> = {
	3: "invalid_argument",
	5: "not_found",
	7: "permission_denied",
	13: "internal",
	14: "unavailable",
	16: "unauthenticated",
};

const CANONICAL_CODES = new Set(Object.values(GRPC_CODE_NAMES));

function canonicalCode(raw: unknown): string | undefined {
	if (typeof raw === "number") return GRPC_CODE_NAMES[raw];
	if (typeof raw === "string") {
		const normalized = raw
			.trim()
			.toLowerCase()
			.replace(/[\s-]+/g, "_");
		return CANONICAL_CODES.has(normalized) ? normalized : undefined;
	}
	return undefined;
}

// When the body carried no usable code, derive one from the HTTP status so the
// message mapping still has something to switch on.
function codeForStatus(status: number): string {
	switch (status) {
		case 400:
			return "invalid_argument";
		case 401:
			return "unauthenticated";
		case 403:
			return "permission_denied";
		case 404:
			return "not_found";
		case 503:
			return "unavailable";
		default:
			return status >= 500 ? "internal" : "unknown";
	}
}

function friendlyMessage(code: string): string {
	switch (code) {
		case "unauthenticated":
			return "We couldn't verify your sign-in. Please start over and try again.";
		case "permission_denied":
			return "Your account isn't permitted to access this application.";
		case "unavailable":
			return "Sign-in is temporarily unavailable. Please try again in a few minutes.";
		case "invalid_argument":
			return "This sign-in request was invalid or has expired. Please start over.";
		case "not_found":
			return "The sign-in service could not be reached. Please try again.";
		case "internal":
			return "Something went wrong on our side while completing sign-in. Please try again.";
		default:
			return "We couldn't complete your sign-in. Please try again.";
	}
}

// `traceparent` is `version-traceid-spanid-flags`; the 32-hex trace id is the
// second field. `x-trace-id` (when a proxy sets it) is the bare id.
function readTraceId(response: Response): string | undefined {
	const traceparent = response.headers.get("traceparent");
	if (traceparent) {
		const parts = traceparent.split("-");
		if (parts.length >= 3 && /^[0-9a-f]{32}$/i.test(parts[1])) {
			return parts[1];
		}
	}
	const direct = response.headers.get("x-trace-id");
	return direct?.trim() || undefined;
}

function parseErrorBody(body: string): { code?: string; message?: string } {
	const trimmed = body.trim();
	if (!trimmed) return {};
	try {
		const parsed = JSON.parse(trimmed) as {
			code?: unknown;
			message?: unknown;
			error?: unknown;
		};
		const message =
			typeof parsed.message === "string"
				? parsed.message
				: typeof parsed.error === "string"
					? parsed.error
					: undefined;
		return { code: canonicalCode(parsed.code), message };
	} catch {
		return {};
	}
}

/**
 * Build an {@link AuthError} from a failed auth response: friendly copy chosen
 * from the backend code/status, plus the correlation metadata an operator needs.
 */
export async function authErrorFromResponse(
	response: Response,
): Promise<AuthError> {
	const body = await response.text().catch(() => "");
	const parsed = parseErrorBody(body);
	const code = parsed.code ?? codeForStatus(response.status);
	return new AuthError(friendlyMessage(code), {
		status: response.status,
		code: parsed.code,
		requestId: response.headers.get("x-request-id")?.trim() || undefined,
		traceId: readTraceId(response),
	});
}

/**
 * A compact, operator-facing reference for a failed sign-in. Always returns
 * something to show — even the deliberately-opaque credential failures carry a
 * request id that ties the browser's rejection to the server log line with the
 * real cause.
 */
export function operatorReference(detail: AuthErrorDetail): string {
	const parts: string[] = [detail.code ?? `HTTP ${detail.status}`];
	if (detail.requestId) parts.push(`request ${detail.requestId}`);
	if (detail.traceId) parts.push(`trace ${detail.traceId}`);
	return parts.join(" · ");
}
