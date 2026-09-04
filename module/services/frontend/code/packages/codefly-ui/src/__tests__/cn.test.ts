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
