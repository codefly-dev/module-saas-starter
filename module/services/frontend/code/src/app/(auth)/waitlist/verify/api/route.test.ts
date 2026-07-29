import { afterEach, describe, expect, it, vi } from "vitest";

const cookieStore = vi.hoisted(() => ({
	get: vi.fn(() => ({ value: "verification-token" })),
}));

vi.mock("next/headers", () => ({
	cookies: async () => cookieStore,
}));

import { POST } from "./route";

afterEach(() => {
	vi.restoreAllMocks();
	cookieStore.get.mockReturnValue({ value: "verification-token" });
});

describe("waitlist verification token lifecycle", () => {
	it("retains the HttpOnly token when the provider is temporarily unavailable", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(JSON.stringify({ error: "unavailable" }), { status: 503 }),
		);

		const response = await POST(
			new Request("https://app.example/waitlist/verify/api", {
				method: "POST",
			}),
		);

		expect(response.status).toBe(503);
		expect(response.headers.get("set-cookie")).toBeNull();
	});

	it("consumes the token after a definitive verification response", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(
				JSON.stringify({
					state: "WAITLIST_STATE_VERIFIED",
					message: "Verified",
				}),
				{ status: 200 },
			),
		);

		const response = await POST(
			new Request("https://app.example/waitlist/verify/api", {
				method: "POST",
			}),
		);

		expect(response.status).toBe(200);
		expect(response.headers.get("set-cookie")).toContain(
			"waitlist_verification_token=",
		);
	});
});
