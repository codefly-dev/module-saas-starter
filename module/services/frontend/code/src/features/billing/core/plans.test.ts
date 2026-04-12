import { describe, it, expect } from "vitest";
import { PRICING_PLANS } from "./plans";

describe("PRICING_PLANS", () => {
  it("has three plans", () => {
    expect(PRICING_PLANS).toHaveLength(3);
  });

  it("has unique plan names", () => {
    const names = PRICING_PLANS.map((p) => p.name);
    expect(new Set(names).size).toBe(names.length);
  });

  it("exactly one highlighted plan", () => {
    const highlighted = PRICING_PLANS.filter((p) => p.highlighted);
    expect(highlighted).toHaveLength(1);
    expect(highlighted[0].name).toBe("pro");
  });

  it("every plan has at least one feature", () => {
    for (const p of PRICING_PLANS) {
      expect(p.features.length).toBeGreaterThan(0);
    }
  });

  it("ctas map correctly to plan names", () => {
    const byName = Object.fromEntries(PRICING_PLANS.map((p) => [p.name, p]));
    expect(byName.free.cta).toBe("start_free");
    expect(byName.pro.cta).toBe("upgrade");
    expect(byName.enterprise.cta).toBe("contact");
  });
});
