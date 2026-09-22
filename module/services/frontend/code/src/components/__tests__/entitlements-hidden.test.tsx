import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import EntitlementsRoute from "@/app/admin/entitlements/page";
import PlatformPage from "@/app/admin/platform/page";
import { selectNavigation } from "@/lib/plugins/presentation";
import { defineFrontend } from "@codefly/saas-plugin-contract";

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

it("hides the platform overview shortcut", () => {
	render(<PlatformPage />);
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
			selectNavigation(config, surface, {
				isAuthenticated: true,
				platformRole: "super_admin",
			}).map((item) => item.href),
		).toEqual(["/admin/teams"]);
	},
);
