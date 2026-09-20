import { describe, expect, it } from "vitest";
import { cn as chatCn } from "../chat/cn.js";
import { cn as dashboardCn } from "../dashboard/cn.js";
import { cn as layoutCn } from "../layout/cn.js";

// The kit ships one `cn` per subpath (`layout`, `dashboard`, `chat`) so each
// bundles independently, but all three MUST behave identically: clsx + tailwind
// -merge, so a caller's `className` (passed last) wins conflicting Tailwind
// utilities. A subpath that regresses to a plain truthy-join would emit both the
// component's class and the caller's, and CSS source-order — not the caller —
// would decide, silently ignoring the override. This guards against exactly that
// divergence (it fails against a `parts.filter(Boolean).join(" ")` join).
const implementations: Array<{ tier: string; cn: typeof layoutCn }> = [
	{ tier: "layout", cn: layoutCn },
	{ tier: "dashboard", cn: dashboardCn },
	{ tier: "chat", cn: chatCn },
];

describe.each(implementations)("$tier/cn", ({ cn }) => {
	it("resolves conflicting utilities with the last one winning", () => {
		// A plain join returns "p-4 p-2"; tailwind-merge collapses to the last.
		expect(cn("p-4", "p-2")).toBe("p-2");
	});

	it("lets a caller's className override a component's own class", () => {
		expect(cn("rounded-lg border p-4", "rounded-full")).toBe(
			"border p-4 rounded-full",
		);
	});

	it("still drops falsy parts and joins non-conflicting classes", () => {
		expect(cn("text-sm", false, null, undefined, "font-bold")).toBe(
			"text-sm font-bold",
		);
	});
});

// The skin's slot and rung utilities are custom AND composite: one class sets
// several properties. tailwind-merge can only keep or drop a whole class, so the
// one thing `cn` must never do is treat a caller's single-property override as a
// conflict with a slot — `text-lg` would then delete the slot's weight, line
// height and family, and `h-11` would strip a button's padding and text. The
// generated utilities sit at zero specificity instead (`:where(&)`), so keeping
// both classes is what makes the core utility override exactly one property.
describe.each(implementations)(
	"$tier/cn knows the skin utilities",
	({ cn }) => {
		it("keeps a raw utility BESIDE a type slot; the cascade overrides one property", () => {
			expect(cn("type-card-title", "text-lg")).toBe("type-card-title text-lg");
			expect(cn("type-card-title", "font-bold")).toBe(
				"type-card-title font-bold",
			);
			expect(cn("type-table-head", "text-xs")).toBe("type-table-head text-xs");
			expect(cn("font-mono tracking-[0.3em]", "md:type-input")).toBe(
				"font-mono tracking-[0.3em] md:type-input",
			);
		});

		it("keeps a raw utility beside a control rung", () => {
			expect(cn("control-default", "h-10")).toBe("control-default h-10");
			expect(cn("control-lg", "px-2")).toBe("control-lg px-2");
			expect(cn("control-icon-sm", "size-8")).toBe("control-icon-sm size-8");
			expect(cn("control-height-default", "h-10")).toBe(
				"control-height-default h-10",
			);
		});

		it("resolves one slot against another", () => {
			expect(cn("type-card-title", "type-page-title")).toBe("type-page-title");
		});

		it("resolves one full rung against another", () => {
			expect(cn("control-sm", "control-lg")).toBe("control-lg");
			expect(cn("control-default", "control-icon-sm")).toBe("control-icon-sm");
			expect(cn("control-icon-sm", "control-default")).toBe("control-default");
			expect(cn("control-height-default", "control-sm")).toBe("control-sm");
		});

		// A height-only rung after a full rung is an override of the height alone;
		// it is emitted after the full rung, so keeping both is the right answer.
		it("keeps a later height-only rung beside a full rung", () => {
			expect(cn("control-sm", "control-height-default")).toBe(
				"control-sm control-height-default",
			);
		});

		// A slot beside a rung sets overlapping text properties on purpose: the slot
		// is emitted after the rung and wins by source order. Neither deletes the
		// other, so an input keeps its height when its text takes a slot.
		it("keeps a type slot beside any rung", () => {
			expect(cn("control-height-default", "type-input-touch")).toBe(
				"control-height-default type-input-touch",
			);
			expect(cn("control-default", "type-body")).toBe(
				"control-default type-body",
			);
		});

		it("keeps utilities the slot does not set", () => {
			expect(cn("type-card-title", "text-muted-foreground")).toBe(
				"type-card-title text-muted-foreground",
			);
			expect(cn("control-default", "rounded-lg")).toBe(
				"control-default rounded-lg",
			);
		});
	},
);
