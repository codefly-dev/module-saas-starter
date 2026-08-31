"use client";

import {
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	scopedDashboardDraftKey,
	USER_DASHBOARD_DRAFT_KEY,
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

/**
 * Binds the host's dashboard-authoring capability to a mounted solution and
 * injects it into the remote. The handle commits to the viewer's own dashboard
 * draft — the SAME viewer-scoped key the Dashboards editor renders from — so an
 * external driver's `setDashboard` durably changes the user's dashboard and the
 * canvas reflects it (live across tabs through the draft's storage subscription,
 * and on next open otherwise). The channel carries no canvas of its own: the
 * host already owns that surface, and rendering the user's dashboard on every
 * solution page would be both redundant and intrusive.
 *
 * The draft is bound here (not inside the generic SolutionOutlet) so the outlet
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
		"getAccessToken" | "refreshAccessToken" | "dashboardAuthoring"
	>;
}) {
	const { user, organizationId } = useAuth();
	const draftKey = scopedDashboardDraftKey(USER_DASHBOARD_DRAFT_KEY, {
		organizationId,
		userId: user?.id,
	});
	const { authoring } = useDashboardAuthoring(draftKey, EMPTY_DASHBOARD);
	return (
		<SolutionOutlet
			remote={remote}
			pageProps={pageProps}
			authoring={authoring}
		/>
	);
}
