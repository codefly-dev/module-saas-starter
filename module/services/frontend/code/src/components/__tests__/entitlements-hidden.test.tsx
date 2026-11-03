import { defineFrontend } from "@codefly-dev/saas-plugin-contract";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import EntitlementsRoute from "@/app/admin/entitlements/page";
import { selectNavigation } from "@/lib/plugins/presentation";
import { UNCONFIGURED_PUBLIC_RUNTIME_CONFIG } from "@/lib/public-runtime-config";

const mount = vi.hoisted(() => vi.fn(() => <div>Entitlements editor</div>));
vi.mock("@/features/platform/ui/entitlements-page", () => ({
	EntitlementsPage: mount,
}));
afterEach(cleanup);

it("does not mount the entitlement editor through its direct route", () => {
	render(<EntitlementsRoute />);
	expect(screen.getByRole("heading").textContent).toBe(
		"Entitlements is not enabled",
	);
	expect(mount).not.toHaveBeenCalled();
});

// The hand-typed platform overview that used to be asserted here is gone: it
// re-listed what the generated catalog already holds, which `/admin` renders.
// The surface filter below is that one path, checked on every surface.
it.each(["sidebar", "command_palette", "plugin_registry"] as const)(
	"hides entitlement navigation from %s, even for super admins",
	(surface) => {
		const config = {
			...defineFrontend({
				branding: {
					name: "Example",
					mark: "E",
					title: "Example",
					description: "Example",
				},
				plugins: [],
			}),
			navItems: [
				{ label: "Entitlements", href: "/admin/entitlements" },
				{ label: "Teams", href: "/admin/teams" },
			],
		};
		expect(
			selectNavigation(
				config,
				surface,
				{ isAuthenticated: true, platformRole: "super_admin" },
				UNCONFIGURED_PUBLIC_RUNTIME_CONFIG.productFeatures,
			).map((item) => item.href),
		).toEqual(["/admin/teams"]);
	},
);
