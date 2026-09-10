import { describe, it, expect } from "vitest";
import { GET } from "./route";

describe("GET /api/healthz", () => {
  it("returns 200 so the startup probe passes", async () => {
    const response = GET();
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({ status: "ok" });
  });
});
