import { afterEach, describe, expect, it, vi } from "vitest";

const cookieStore = vi.hoisted(() => ({
	get: vi.fn(() => undefined),
}));

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
			new URL(
				"/v1/invitations:inspect-id",
				"https://app.example/invitations/accept/api",
			),
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
