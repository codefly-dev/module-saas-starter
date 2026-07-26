import { describe, expect, it } from "vitest";
import {
	SIDEBAR_NAVIGATION,
	USER_MENU_NAVIGATION,
} from "@/gen/saas/frontend/v1/plugin_catalog";
import {
	groupFrontendNavigation,
	isFrontendNavigationVisible,
} from "@/lib/frontend-navigation";

describe("generated frontend navigation", () => {
	it("applies the finite route audience consistently", () => {
		expect(
			isFrontendNavigationVisible("authenticated", undefined, "member"),
		).toBe(true);
		expect(isFrontendNavigationVisible("admin", undefined, "member")).toBe(
			false,
		);
		expect(isFrontendNavigationVisible("admin", undefined, "admin")).toBe(true);
		expect(isFrontendNavigationVisible("admin", "support", undefined)).toBe(
			true,
		);
		expect(
			isFrontendNavigationVisible("super_admin", "support", undefined),
		).toBe(false);
		expect(
			isFrontendNavigationVisible("super_admin", "super_admin", undefined),
		).toBe(true);
	});

	it("preserves generated order when grouping the admin sidebar", () => {
		const groups = groupFrontendNavigation(
			SIDEBAR_NAVIGATION.filter(
				(item) => item.access === "admin" || item.access === "super_admin",
			),
		);
		expect(groups.map((group) => group.group)).toEqual([
			"Users & Access",
			"Platform",
			"Billing",
			"Integrations",
			"Developer",
		]);
		expect(groups[0]?.items.map((item) => item.href)).toEqual([
			"/admin/users",
			"/admin/organizations",
			"/admin/teams",
			"/admin/roles",
			"/admin/invitations",
			"/admin/api-keys",
		]);
	});

	it("keeps general Settings directly discoverable", () => {
		expect(
			SIDEBAR_NAVIGATION.find((item) => item.id === "settings"),
		).toMatchObject({
			label: "Settings",
			href: "/settings",
			access: "authenticated",
		});
		expect(USER_MENU_NAVIGATION[0]?.id).toBe("settings");
	});
});
