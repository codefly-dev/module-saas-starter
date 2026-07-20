import type {
	FrontendNavigationItem,
	FrontendRouteAccess,
} from "@/gen/saas/frontend/v1/plugin_catalog";
import type { OrgRole, PlatformRole } from "@/lib/auth-session";
import { canPresent, groupNavigation } from "@/lib/plugins/presentation";

export function isFrontendNavigationVisible(
	access: FrontendRouteAccess,
	platformRole: PlatformRole | undefined,
	orgRole: OrgRole | undefined,
): boolean {
	return canPresent(
		{ access },
		{ isAuthenticated: true, platformRole, orgRole },
	);
}

export interface FrontendNavigationGroup {
	readonly group: string;
	readonly items: readonly FrontendNavigationItem[];
}

export function groupFrontendNavigation(
	items: readonly FrontendNavigationItem[],
): FrontendNavigationGroup[] {
	return groupNavigation(items) as FrontendNavigationGroup[];
}
