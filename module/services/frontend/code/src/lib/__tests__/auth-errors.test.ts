import { describe, expect, it } from "vitest";
import {
	AuthError,
	authErrorFromResponse,
	operatorReference,
} from "@/lib/auth-errors";

function errorResponse(
	status: number,
	body: unknown,
	headers: Record<string, string> = {},
): Response {
	return new Response(typeof body === "string" ? body : JSON.stringify(body), {
		status,
		headers,
	});
}

describe("authErrorFromResponse", () => {
	it("maps a collapsed credential rejection to friendly copy and keeps the request id", async () => {
		// Every distinct backend cause (bad DNS, token-endpoint 401, missing
		// email, audience mismatch) arrives here as the same 401/16/'invalid
		// credentials' — the request id is the only handle back to the real cause.
		const error = await authErrorFromResponse(
			errorResponse(
				401,
				{ code: 16, message: "invalid credentials" },
				{ "x-request-id": "req-abc123" },
			),
		);

		expect(error).toBeInstanceOf(AuthError);
		expect(error.message).toContain("verify your sign-in");
		expect(error.message).not.toContain("invalid credentials");
		expect(error.detail).toMatchObject({
			status: 401,
			code: "unauthenticated",
			requestId: "req-abc123",
			// The backend reason is preserved (generic here, the real cause in dev)
			// so it can be surfaced on the reference line rather than discarded.
			backendMessage: "invalid credentials",
		});
	});

	it("preserves the backend's real reason exposed in local development", async () => {
		// In local dev the accounts service returns the underlying reason in the
		// message field instead of the collapsed string; it must survive to the UI.
		const error = await authErrorFromResponse(
			errorResponse(
				401,
				{ code: 16, message: "auth: token audience mismatch" },
				{ "x-request-id": "req-dev" },
			),
		);

		expect(error.detail.backendMessage).toBe("auth: token audience mismatch");
		expect(operatorReference(error.detail)).toContain(
			"auth: token audience mismatch",
		);
	});

	it("distinguishes a denied group gate from a credential failure", async () => {
		const error = await authErrorFromResponse(
			errorResponse(403, { code: 7, message: "access not granted" }),
		);

		expect(error.message).toContain("isn't permitted");
		expect(error.detail.code).toBe("permission_denied");
	});

	it("distinguishes a retryable service outage", async () => {
		const error = await authErrorFromResponse(
			errorResponse(503, {
				code: 14,
				message: "authentication temporarily unavailable",
			}),
		);

		expect(error.message).toContain("temporarily unavailable");
		expect(error.detail.code).toBe("unavailable");
	});

	it("distinguishes a genuine server failure", async () => {
		const error = await authErrorFromResponse(
			errorResponse(500, { code: 13, message: "boom" }),
		);

		expect(error.message).toContain("Something went wrong");
		expect(error.detail.code).toBe("internal");
	});

	it("falls back to the HTTP status when the body carries no usable code", async () => {
		const error = await authErrorFromResponse(
			errorResponse(401, "upstream is grumpy", {
				"x-request-id": "req-xyz",
			}),
		);

		// No parsed code, but the status still selects the unauthenticated copy.
		expect(error.message).toContain("verify your sign-in");
		expect(error.detail.code).toBeUndefined();
		expect(error.detail.requestId).toBe("req-xyz");
	});

	it("parses a trace id out of the traceparent header", async () => {
		const traceId = "0af7651916cd43dd8448eb211c80319c";
		const error = await authErrorFromResponse(
			errorResponse(
				401,
				{ code: 16, message: "invalid credentials" },
				{ traceparent: `00-${traceId}-b7ad6b7169203331-01` },
			),
		);

		expect(error.detail.traceId).toBe(traceId);
	});
});

describe("operatorReference", () => {
	it("combines code, request id, and trace id", () => {
		expect(
			operatorReference({
				status: 401,
				code: "unauthenticated",
				requestId: "req-abc",
				traceId: "trace-1",
			}),
		).toBe("unauthenticated · request req-abc · trace trace-1");
	});

	it("always yields a reference even with only a status", () => {
		expect(operatorReference({ status: 500 })).toBe("HTTP 500");
	});
});
