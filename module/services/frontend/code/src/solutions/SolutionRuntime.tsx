"use client";

import { useMemo } from "react";
import {
	createBrowserDashboardLibrary,
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	driverDashboardStore,
	scopedDashboardDraftKey,
	USER_DASHBOARD_LIBRARY_KEY,
	useDashboardAuthoring,
} from "@/features/dashboard";
import { useAuth } from "@/lib/auth";
import {
	SolutionOutlet,
	type SolutionPageProps,
	type SolutionRemote,
} from "./SolutionOutlet";

const EMPTY_DASHBOARD: DashboardDef = {
	version: DASHBOARD_SPEC_VERSION,
	metrics: [],
};

// The reserved record the external-driver channel writes to. A fixed id (never
// a generated UUID) lets a freshly-mounted solution page bind the same record
// every time without persisting which id it is; the name is what the viewer
// sees for it in "My Dashboards".
export const EXTERNAL_DASHBOARD_ID = "external-driver";
export const EXTERNAL_DASHBOARD_NAME = "AI dashboard";

/**
 * Binds the host's dashboard-authoring capability to a mounted solution and
 * injects it into the remote. The handle commits to a reserved record in the
 * viewer's dashboard library — the SAME collection the "My Dashboards" surface
 * lists and opens — so an external driver's `setDashboard` durably creates or
 * updates a dashboard the user can find, open, rename, and share, and the canvas
 * reflects it (live across tabs through the store's subscription, and on next
 * open otherwise). The channel carries no canvas of its own: the host already
 * owns that surface, and rendering the user's dashboard on every solution page
 * would be both redundant and intrusive.
 *
 * The store is bound here (not inside the generic SolutionOutlet) so the outlet
 * stays pure Module Federation infra with no dependency on the app's auth,
 * audit, or query providers.
 */
export function SolutionRuntime({
	remote,
	pageProps,
}: {
	remote: SolutionRemote;
	pageProps: Omit<
		SolutionPageProps,
		| "getAccessToken"
		| "refreshAccessToken"
		| "authedFetch"
		| "dashboardAuthoring"
	>;
}) {
	const { user, organizationId } = useAuth();
	const libraryKey = scopedDashboardDraftKey(USER_DASHBOARD_LIBRARY_KEY, {
		organizationId,
		userId: user?.id,
	});
	// Memoized so the injected store keeps a stable identity across renders; the
	// draft hook re-subscribes whenever the store changes.
	const store = useMemo(
		() =>
			driverDashboardStore(createBrowserDashboardLibrary(libraryKey), {
				id: EXTERNAL_DASHBOARD_ID,
				name: EXTERNAL_DASHBOARD_NAME,
			}),
		[libraryKey],
	);
	const { authoring } = useDashboardAuthoring(
		EXTERNAL_DASHBOARD_ID,
		EMPTY_DASHBOARD,
		store,
	);
	return (
		<SolutionOutlet
			remote={remote}
			pageProps={pageProps}
			authoring={authoring}
		/>
	);
}
