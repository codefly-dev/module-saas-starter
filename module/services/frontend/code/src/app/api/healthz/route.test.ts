import { afterEach, describe, expect, it } from "vitest";
import { GET } from "./route";

describe("GET /api/healthz", () => {
	const original = process.env.PRODUCT_GATEWAY_INTERNAL;

	afterEach(() => {
		if (original === undefined) delete process.env.PRODUCT_GATEWAY_INTERNAL;
		else process.env.PRODUCT_GATEWAY_INTERNAL = original;
	});

	it("returns 200 so the startup probe passes", async () => {
		process.env.PRODUCT_GATEWAY_INTERNAL = "http://auth-gateway.internal";
		const response = GET();
		expect(response.status).toBe(200);
		await expect(response.json()).resolves.toEqual({ status: "ok" });
	});

	it("stays unready while no product API gateway resolves", async () => {
		delete process.env.PRODUCT_GATEWAY_INTERNAL;
		const response = GET();
		expect(response.status).toBe(503);
		await expect(response.json()).resolves.toEqual({
			status: "misconfigured",
			reason: expect.stringContaining("auth-gateway/rest"),
		});
	});
});
