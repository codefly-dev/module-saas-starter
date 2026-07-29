import { afterEach, describe, expect, it, vi } from "vitest";
import { billingMutations } from "./mutations";

afterEach(() => {
	vi.restoreAllMocks();
});

describe("billingMutations", () => {
	it("starts checkout through the server-owned plan catalog", async () => {
		const request = vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(JSON.stringify({ url: "https://checkout.example/session" }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);

		const url = await billingMutations.startCheckout("access-token", "pro");

		expect(url).toBe("https://checkout.example/session");
		expect(request).toHaveBeenCalledWith(
			"/v1/billing/checkout",
			expect.objectContaining({
				method: "POST",
				body: JSON.stringify({ plan_name: "pro" }),
				headers: expect.objectContaining({
					Authorization: "Bearer access-token",
				}),
			}),
		);
	});

	it("activates the free plan through a persisted server action", async () => {
		const request = vi.spyOn(globalThis, "fetch").mockResolvedValue(
			new Response(JSON.stringify({ status: "active" }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);

		await billingMutations.selectFreePlan("access-token");

		expect(request).toHaveBeenCalledWith(
			"/v1/billing/free-plan",
			expect.objectContaining({
				method: "POST",
				headers: expect.objectContaining({
					Authorization: "Bearer access-token",
				}),
			}),
		);
	});
});
