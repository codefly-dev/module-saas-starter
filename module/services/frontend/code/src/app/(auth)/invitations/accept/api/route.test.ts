import { afterEach, describe, expect, it, vi } from "vitest";

const cookieStore = vi.hoisted(() => ({
	get: vi.fn(() => undefined),
}));

const { getEndpoints } = vi.hoisted(() => ({
	getEndpoints: vi.fn(() => [
		{ service: "auth-gateway", name: "rest", address: "http://auth-gateway.internal:8080" },
	]),
}));
vi.mock("codefly", () => ({ getEndpoints }));
vi.mock("next/headers", () => ({
	cookies: async () => cookieStore,
}));

import { POST } from "./route";

afterEach(() => {
	vi.restoreAllMocks();
	cookieStore.get.mockReturnValue(undefined);
});

describe("invitation acceptance API", () => {
	it("inspects an in-app invitation ID with the authenticated account", async () => {
		const upstream = vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(JSON.stringify({ status: "INVITATION_STATUS_REVOKED" }), {
				status: 200,
			}),
		);

		const response = await POST(
			new Request("https://app.example/invitations/accept/api", {
				method: "POST",
				headers: {
					Authorization: "Bearer access-token",
					"Content-Type": "application/json",
				},
				body: JSON.stringify({
					action: "inspect",
					invitationId: "00000000-0000-4000-8000-000000000030",
				}),
			}),
		);

		expect(response.status).toBe(200);
		expect(await response.json()).toEqual({
			status: "INVITATION_STATUS_REVOKED",
		});
		expect(upstream).toHaveBeenCalledWith(
			"http://auth-gateway.internal:8080/v1/invitations:inspect-id",
			expect.objectContaining({
				headers: expect.objectContaining({
					Authorization: "Bearer access-token",
				}),
				body: JSON.stringify({
					invitationId: "00000000-0000-4000-8000-000000000030",
				}),
			}),
		);
	});
});

// A server route dials the gateway the composition declares, never the origin
// the incoming request names: that origin terminates TLS at the cell's edge and
// is unreachable from inside the pod, and a Host header is client-controlled.
// With no gateway declared the route refuses visibly instead of guessing.
describe("the route reaches the gateway by its composed endpoint", () => {
	it("refuses with 503 when the composition states no gateway", async () => {
		getEndpoints.mockReturnValueOnce([]);
		const upstream = vi.spyOn(globalThis, "fetch");
		const response = await POST(
			new Request("https://app.example/invitations/accept/api", {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ action: "accept", invitationId: "11111111-1111-4111-8111-111111111111" }),
			}),
		);
		expect(response.status).toBe(503);
		expect(upstream).not.toHaveBeenCalled();
	});
});
