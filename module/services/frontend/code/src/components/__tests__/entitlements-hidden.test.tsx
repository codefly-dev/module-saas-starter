import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { UNCONFIGURED_PUBLIC_RUNTIME_CONFIG } from "@/lib/public-runtime-config";
import EntitlementsRoute from "@/app/admin/entitlements/page";
import PlatformPage from "@/app/admin/platform/page";
import { selectNavigation } from "@/lib/plugins/presentation";
import { defineFrontend } from "@codefly/saas-plugin-contract";

// The platform overview reads the deployment's switches per request; an
// unconfigured deployment leaves every optional surface off.
vi.mock("@/lib/read-public-runtime-config", () => ({
	readPublicRuntimeConfig: async () => UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
}));
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

it("hides the platform overview shortcut", async () => {
	render(await PlatformPage());
	expect(screen.queryByRole("link", { name: /Entitlements/ })).toBeNull();
	expect(screen.getByRole("link", { name: /Sessions/ })).toBeTruthy();
});

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
