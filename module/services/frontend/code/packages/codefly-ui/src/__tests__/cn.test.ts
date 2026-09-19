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


// The skin's slot and rung utilities are custom, so stock tailwind-merge does not
// know what they conflict with: it would keep both a slot class and a caller's
// `text-lg` and let CSS source order decide the winner. That is exactly the
// silent override failure `cn` exists to prevent, and it would only show up as a
// component that ignores its `className` on some builds.
describe.each(implementations)("$tier/cn knows the skin utilities", ({ cn }) => {
	it("lets a caller's raw utility override a type slot", () => {
		expect(cn("type-card-title", "text-lg")).toBe("text-lg");
		expect(cn("type-card-title", "font-bold")).toBe("font-bold");
		expect(cn("type-card-title", "tracking-tight")).toBe("tracking-tight");
	});

	it("lets a type slot override the raw utilities it replaces", () => {
		expect(cn("text-sm font-medium leading-none", "type-card-title")).toBe(
			"type-card-title",
		);
	});

	it("resolves one slot against another", () => {
		expect(cn("type-card-title", "type-page-title")).toBe("type-page-title");
	});

	it("lets a caller resize a control rung", () => {
		expect(cn("control-default", "h-10")).toBe("h-10");
		expect(cn("control-sm", "control-lg")).toBe("control-lg");
		expect(cn("control-default", "text-lg")).toBe("text-lg");
	});

	// The groups are declared by the properties each utility SETS. Lumping the
	// four control shapes together made a type slot delete a height-only rung
	// standing beside it, which is how an input loses its height while its source
	// still reads as migrated.
	it("keeps a height-only rung beside a type slot", () => {
		expect(cn("control-height-default", "type-input-touch")).toBe(
			"control-height-default type-input-touch",
		);
		expect(cn("control-height-default", "type-sidebar-menu-button")).toBe(
			"control-height-default type-sidebar-menu-button",
		);
	});

	it("still resolves rungs that do overlap", () => {
		expect(cn("control-height-default", "control-sm")).toBe("control-sm");
		expect(cn("control-default", "control-icon-sm")).toBe("control-icon-sm");
		expect(cn("control-height-default", "h-10")).toBe("h-10");
	});

	it("keeps utilities the slot does not set", () => {
		expect(cn("type-card-title", "text-muted-foreground")).toBe(
			"type-card-title text-muted-foreground",
		);
		expect(cn("control-default", "rounded-lg")).toBe(
			"control-default rounded-lg",
		);
	});
});
