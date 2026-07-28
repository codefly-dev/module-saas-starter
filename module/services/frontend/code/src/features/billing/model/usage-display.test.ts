import { describe, expect, it } from "vitest";
import {
	normalizeUsageSeries,
	projectUsage,
	usageHistoryPresentation,
	usagePercent,
	usageTone,
} from "./usage-display";

describe("usage display calculations", () => {
	it("preserves int64 values above JavaScript's safe integer boundary", () => {
		const used = BigInt("9007199254740993");
		const limit = used * BigInt(2);

		expect(usagePercent(used, limit)).toBe(50);
		expect(usageTone(used, limit)).toBe("healthy");
		expect(projectUsage(used, 2, 1)).toBe(BigInt("18014398509481986"));
		expect(normalizeUsageSeries([used, limit])).toEqual([500_000, 1_000_000]);
	});

	it("keeps loading distinct from a completed empty history", () => {
		expect(usageHistoryPresentation(true, false, 0, BigInt(0))).toBe("loading");
		expect(usageHistoryPresentation(false, false, 0, BigInt(0))).toBe(
			"no_data",
		);
	});
});
